package daemon

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"

	"goodkind.io/agent-gate/api/daemonpb"
)

type deadlineRecordingAgentGateClient struct {
	daemonpb.AgentGateDClient
	remaining time.Duration
}

func (client *deadlineRecordingAgentGateClient) EvaluateHook(
	ctx context.Context,
	_ *daemonpb.EvaluateHookRequest,
	_ ...grpc.CallOption,
) (*daemonpb.EvaluateHookResponse, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return &daemonpb.EvaluateHookResponse{}, nil
	}
	client.remaining = time.Until(deadline)
	return &daemonpb.EvaluateHookResponse{}, nil
}

func TestClientEvaluateHookUsesTwelveSecondDeadline(t *testing.T) {
	rpc := &deadlineRecordingAgentGateClient{}
	client := &Client{rpc: rpc}

	if _, err := client.EvaluateHook(nil, "codex", "", nil, nil); err != nil {
		t.Fatalf("EvaluateHook returned error: %v", err)
	}
	if rpc.remaining < 11*time.Second || rpc.remaining > 12*time.Second {
		t.Fatalf("EvaluateHook deadline remaining = %s, want between 11s and 12s", rpc.remaining)
	}
}
