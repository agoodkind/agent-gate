package daemon

import (
	"context"
	"fmt"
	"log/slog"
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
	log := slog.Default()
	socketPath := config.DaemonSocketPath()
	target := "unix://" + socketPath

	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.ErrorContext(ctx, "connect to daemon failed", "socket", socketPath, "err", err)
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
	if err := c.conn.Close(); err != nil {
		return fmt.Errorf("close daemon client: %w", err)
	}
	return nil
}

// EvaluateHook forwards raw hook input to daemon-owned enforcement.
func (c *Client) EvaluateHook(rawJSON []byte, providerHint, cwd string, argv []string, env map[string]string) (*daemonpb.EvaluateHookResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := c.rpc.EvaluateHook(ctx, &daemonpb.EvaluateHookRequest{
		RawJson:        rawJSON,
		ProviderHint:   providerHint,
		Cwd:            cwd,
		Argv:           argv,
		EnvFingerprint: env,
	})
	if err != nil {
		return nil, fmt.Errorf("daemon EvaluateHook rpc: %w", err)
	}
	return resp, nil
}

// Status fetches the daemon's identifying information via the Status RPC.
func (c *Client) Status() (*daemonpb.StatusResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.rpc.Status(ctx, &daemonpb.StatusRequest{})
	if err != nil {
		return nil, fmt.Errorf("daemon Status rpc: %w", err)
	}
	return resp, nil
}
