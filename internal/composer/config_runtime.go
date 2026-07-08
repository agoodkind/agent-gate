package composer

import (
	"fmt"
	"io"
	"log/slog"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/clyde/api/contextpb"
	"goodkind.io/lm-review/api/judgepb"
)

// NewRuntimeFromConfig builds a composer runtime from agent-gate config.
func NewRuntimeFromConfig(cfg *config.Config) (*Runtime, error) {
	var closers []io.Closer
	judgeClient, judgeCloser, err := newJudgeClient(cfg)
	if err != nil {
		return nil, err
	}
	if judgeCloser != nil {
		closers = append(closers, judgeCloser)
	}
	clydeClient, clydeCloser, err := newClydeClient(cfg)
	if err != nil {
		closeAll(closers)
		return nil, err
	}
	if clydeCloser != nil {
		closers = append(closers, clydeCloser)
	}
	runtime := NewRuntime(RuntimeOptions{
		Deps: Deps{
			Clyde:         clydeClient,
			IndexedRoots:  DefaultIndexedRootsSource(),
			WorktreeState: nil,
		},
		JudgeClient:         judgeClient,
		JudgeEnabled:        cfg.JudgeEnabled(),
		Authority:           Authority(cfg.JudgeAuthority()),
		Oracle:              nil,
		Judge:               nil,
		RuleSetLister:       nil,
		DisagreementLogPath: cfg.JudgeDisagreementLogPath(),
		Now:                 nil,
	})
	runtime.closers = closers
	return runtime, nil
}

func newJudgeClient(cfg *config.Config) (judgepb.JudgeClient, io.Closer, error) {
	address := cfg.JudgeLMReviewGRPCAddress()
	if strings.TrimSpace(address) == "" {
		return nil, nil, nil
	}
	conn, err := grpc.NewClient(grpcTarget(address), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		slog.Warn("create lm-review judge client failed", "address", address, "err", err)
		return nil, nil, fmt.Errorf("create lm-review judge client: %w", err)
	}
	return judgepb.NewJudgeClient(conn), conn, nil
}

func newClydeClient(cfg *config.Config) (contextpb.ConversationContextClient, io.Closer, error) {
	address := cfg.JudgeClydeGRPCAddress()
	if strings.TrimSpace(address) == "" {
		return nil, nil, nil
	}
	conn, err := grpc.NewClient(grpcTarget(address), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		slog.Warn("create clyde context client failed", "address", address, "err", err)
		return nil, nil, fmt.Errorf("create clyde context client: %w", err)
	}
	return contextpb.NewConversationContextClient(conn), conn, nil
}

func grpcTarget(address string) string {
	trimmed := strings.TrimSpace(address)
	if strings.HasPrefix(trimmed, "/") {
		return "unix://" + trimmed
	}
	return trimmed
}

func closeAll(closers []io.Closer) {
	for _, closer := range closers {
		_ = closer.Close()
	}
}
