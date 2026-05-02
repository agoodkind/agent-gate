package daemon

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"

	"goodkind.io/agent-gate/api/daemonpb"
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

	conn.Connect()
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	for {
		state := conn.GetState()
		if state == connectivity.Ready {
			break
		}
		if !conn.WaitForStateChange(probeCtx, state) {
			_ = conn.Close()
			return nil, fmt.Errorf("daemon at %s is not ready", socketPath)
		}
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

// EvaluateHook forwards raw hook input to daemon-owned enforcement.
func (c *Client) EvaluateHook(rawJSON []byte, providerHint, wrapperID, cwd string, argv []string, env map[string]string) (*daemonpb.EvaluateHookResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return c.rpc.EvaluateHook(ctx, &daemonpb.EvaluateHookRequest{
		RawJson:        rawJSON,
		ProviderHint:   providerHint,
		WrapperId:      wrapperID,
		Cwd:            cwd,
		Argv:           argv,
		EnvFingerprint: env,
	})
}

// ReleaseSession notifies the daemon that this wrapper process has exited.
func (c *Client) ReleaseSession(wrapperID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.rpc.ReleaseSession(ctx, &daemonpb.ReleaseSessionRequest{
		WrapperId: wrapperID,
	})
	return err
}

func (c *Client) Status() (*daemonpb.StatusResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.rpc.Status(ctx, &daemonpb.StatusRequest{})
}
