package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
)

// Client is a gRPC client for the agent-gate daemon.
type Client struct {
	conn *grpc.ClientConn
	rpc  daemonpb.AgentGateDClient
}

// Connect opens a connection to the running daemon.
func Connect(ctx context.Context) (*Client, error) {
	socketPath := config.DaemonSocketPath()
	target := "unix://" + socketPath

	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to daemon at %s: %w", socketPath, err)
	}

	return &Client{conn: conn, rpc: daemonpb.NewAgentGateDClient(conn)}, nil
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// AcquireSession asks the daemon to create a fake HOME for this wrapper process.
func (c *Client) AcquireSession(wrapperID, sessionName string) (*daemonpb.AcquireSessionResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return c.rpc.AcquireSession(ctx, &daemonpb.AcquireSessionRequest{
		WrapperId:   wrapperID,
		SessionName: sessionName,
	})
}

// Audit forwards a single audit entry to the daemon. The daemon enqueues
// the entry to its per-conversation log and returns immediately. The call
// has a short timeout so the hook process never stalls on daemon problems.
func (c *Client) Audit(system, sessionID, eventName, level, msg string, attrs map[string]any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	var attrsJSON []byte
	if len(attrs) > 0 {
		var err error
		attrsJSON, err = json.Marshal(attrs)
		if err != nil {
			return fmt.Errorf("marshal audit attrs: %w", err)
		}
	}

	_, err := c.rpc.Audit(ctx, &daemonpb.AuditRequest{
		System:    system,
		SessionId: sessionID,
		EventName: eventName,
		Level:     level,
		Msg:       msg,
		AttrsJson: attrsJSON,
	})
	return err
}

// AuditSink wraps a Client as an audit.Sink. Each Log call sends one
// AuditRequest. Errors are dropped: the hook process must not fail when
// the daemon is unreachable.
type AuditSink struct {
	client *Client
}

// NewAuditSink returns a Sink that forwards entries to the daemon.
func NewAuditSink(c *Client) *AuditSink {
	return &AuditSink{client: c}
}

// Log forwards one entry. Errors are silently dropped.
func (s *AuditSink) Log(_ context.Context, system, sessionID, eventName, level, msg string, attrs map[string]any) {
	if s == nil || s.client == nil {
		return
	}
	_ = s.client.Audit(system, sessionID, eventName, level, msg, attrs)
}

// Close is a no-op. The underlying client connection is owned elsewhere.
func (s *AuditSink) Close() error { return nil }

// Compile-time check that AuditSink satisfies audit.Sink.
var _ audit.Sink = (*AuditSink)(nil)

// ReleaseSession notifies the daemon that this wrapper process has exited.
func (c *Client) ReleaseSession(wrapperID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.rpc.ReleaseSession(ctx, &daemonpb.ReleaseSessionRequest{
		WrapperId: wrapperID,
	})
	return err
}
