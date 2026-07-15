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

const evaluateHookTimeout = 12 * time.Second

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
	log := slog.Default()
	if err := c.conn.Close(); err != nil {
		log.Warn("close daemon client failed", slog.Any("err", err))
		return fmt.Errorf("close daemon client: %w", err)
	}
	return nil
}

// ResolveHookEnvironment asks the daemon which process environment values evaluation needs.
func (c *Client) ResolveHookEnvironment(
	rawJSON []byte,
	providerHint string,
	env map[string]string,
) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), evaluateHookTimeout)
	defer cancel()
	response, err := c.rpc.ResolveHookEnvironment(
		ctx,
		&daemonpb.ResolveHookEnvironmentRequest{
			RawJson: rawJSON, ProviderHint: providerHint, EnvFingerprint: env,
		},
	)
	if err != nil {
		slog.WarnContext(ctx, "daemon ResolveHookEnvironment rpc failed", slog.Any("err", err))
		return nil, fmt.Errorf("daemon ResolveHookEnvironment rpc: %w", err)
	}
	return response.GetReferencedNames(), nil
}

// EvaluateHook forwards raw hook input to daemon-owned enforcement.
func (c *Client) EvaluateHook(rawJSON []byte, providerHint, cwd string, argv []string, env map[string]string) (*daemonpb.EvaluateHookResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), evaluateHookTimeout)
	defer cancel()
	log := slog.Default()

	resp, err := c.rpc.EvaluateHook(ctx, &daemonpb.EvaluateHookRequest{
		RawJson:        rawJSON,
		ProviderHint:   providerHint,
		Cwd:            cwd,
		Argv:           argv,
		EnvFingerprint: env,
	})
	if err != nil {
		log.WarnContext(ctx, "daemon EvaluateHook rpc failed", slog.Any("err", err))
		return nil, fmt.Errorf("daemon EvaluateHook rpc: %w", err)
	}
	return resp, nil
}

// Status fetches the daemon's identifying information via the Status RPC.
func (c *Client) Status() (*daemonpb.StatusResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.StatusContext(ctx)
}

// StatusContext fetches daemon identity within the caller's context.
func (c *Client) StatusContext(ctx context.Context) (*daemonpb.StatusResponse, error) {
	log := slog.Default()
	resp, err := c.rpc.Status(ctx, &daemonpb.StatusRequest{})
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		log.WarnContext(ctx, "daemon Status rpc failed", slog.Any("err", err))
		return nil, fmt.Errorf("daemon Status rpc: %w", err)
	}
	return resp, nil
}

// KVGet fetches one daemon hot cache entry.
func (c *Client) KVGet(namespace string, key string) (*daemonpb.KVGetResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.rpc.KVGet(ctx, &daemonpb.KVGetRequest{Namespace: namespace, Key: key})
	if err != nil {
		slog.WarnContext(ctx, "daemon KVGet rpc failed", slog.Any("err", err))
		return nil, fmt.Errorf("daemon KVGet rpc: %w", err)
	}
	return resp, nil
}

// KVSet stores one daemon hot cache entry.
func (c *Client) KVSet(namespace string, key string, value []byte, mode string, ttlMs int64) (*daemonpb.KVSetResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.rpc.KVSet(ctx, &daemonpb.KVSetRequest{
		Namespace: namespace,
		Key:       key,
		Value:     value,
		Mode:      mode,
		TtlMs:     ttlMs,
	})
	if err != nil {
		slog.WarnContext(ctx, "daemon KVSet rpc failed", slog.Any("err", err))
		return nil, fmt.Errorf("daemon KVSet rpc: %w", err)
	}
	return resp, nil
}

// KVDelete removes one daemon hot cache entry.
func (c *Client) KVDelete(namespace string, key string) (*daemonpb.KVDeleteResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.rpc.KVDelete(ctx, &daemonpb.KVDeleteRequest{Namespace: namespace, Key: key})
	if err != nil {
		slog.WarnContext(ctx, "daemon KVDelete rpc failed", slog.Any("err", err))
		return nil, fmt.Errorf("daemon KVDelete rpc: %w", err)
	}
	return resp, nil
}

// KVExists reports whether one daemon hot cache entry exists.
func (c *Client) KVExists(namespace string, key string) (*daemonpb.KVExistsResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.rpc.KVExists(ctx, &daemonpb.KVExistsRequest{Namespace: namespace, Key: key})
	if err != nil {
		slog.WarnContext(ctx, "daemon KVExists rpc failed", slog.Any("err", err))
		return nil, fmt.Errorf("daemon KVExists rpc: %w", err)
	}
	return resp, nil
}

// KVTTL returns the Redis-style TTL sentinel or remaining whole seconds.
func (c *Client) KVTTL(namespace string, key string) (*daemonpb.KVTTLResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.rpc.KVTTL(ctx, &daemonpb.KVGetRequest{Namespace: namespace, Key: key})
	if err != nil {
		slog.WarnContext(ctx, "daemon KVTTL rpc failed", slog.Any("err", err))
		return nil, fmt.Errorf("daemon KVTTL rpc: %w", err)
	}
	return resp, nil
}

// KVPTTL returns the Redis-style PTTL sentinel or remaining milliseconds.
func (c *Client) KVPTTL(namespace string, key string) (*daemonpb.KVPTTLResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.rpc.KVPTTL(ctx, &daemonpb.KVGetRequest{Namespace: namespace, Key: key})
	if err != nil {
		slog.WarnContext(ctx, "daemon KVPTTL rpc failed", slog.Any("err", err))
		return nil, fmt.Errorf("daemon KVPTTL rpc: %w", err)
	}
	return resp, nil
}

// KVExpire updates one daemon hot cache entry expiry.
func (c *Client) KVExpire(namespace string, key string, ttlMs int64) (*daemonpb.KVExpireResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.rpc.KVExpire(ctx, &daemonpb.KVExpireRequest{Namespace: namespace, Key: key, TtlMs: ttlMs})
	if err != nil {
		slog.WarnContext(ctx, "daemon KVExpire rpc failed", slog.Any("err", err))
		return nil, fmt.Errorf("daemon KVExpire rpc: %w", err)
	}
	return resp, nil
}

// KVGetDelete fetches and removes one daemon hot cache entry.
func (c *Client) KVGetDelete(namespace string, key string) (*daemonpb.KVGetDeleteResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.rpc.KVGetDelete(ctx, &daemonpb.KVGetDeleteRequest{Namespace: namespace, Key: key})
	if err != nil {
		slog.WarnContext(ctx, "daemon KVGetDelete rpc failed", slog.Any("err", err))
		return nil, fmt.Errorf("daemon KVGetDelete rpc: %w", err)
	}
	return resp, nil
}

// KVList lists daemon hot cache entries for one namespace and prefix.
func (c *Client) KVList(namespace string, prefix string, limit int, includeValues bool) (*daemonpb.KVListResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.rpc.KVList(ctx, &daemonpb.KVListRequest{
		Namespace:     namespace,
		Prefix:        prefix,
		Limit:         boundedInt32(limit),
		IncludeValues: includeValues,
	})
	if err != nil {
		slog.WarnContext(ctx, "daemon KVList rpc failed", slog.Any("err", err))
		return nil, fmt.Errorf("daemon KVList rpc: %w", err)
	}
	return resp, nil
}

func boundedInt32(value int) int32 {
	const maxInt32 = int(^uint32(0) >> 1)
	if value < 0 {
		return 0
	}
	if value > maxInt32 {
		return int32(maxInt32)
	}
	return int32(value)
}
