package rules

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/gitbranch"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
)

type guardInferenceServer struct {
	inferencepb.UnimplementedInferenceServer
	mu    sync.Mutex
	calls map[string][]guardInferenceCall
}

type guardInferenceCall struct {
	model           string
	reasoningEffort inferencepb.ReasoningEffort
}

var guardInferenceBlockCommands = map[string]bool{
	`echo /repo/main/go.mod | xargs grep module`: true,
	`sh -c 'D=/repo/main; grep -rn foo "$D"'`:    true,
	`eval "$(printf 'grep -rn foo /repo/main')"`: true,
	`grep module "$(printf /repo/main/go.mod)"`:  true,
	`bash -c 'rg TODO /repo/main'`:               true,
	`zsh -c 'git -C /repo/main grep TODO'`:       true,
}

func (server *guardInferenceServer) Infer(
	_ context.Context,
	request *inferencepb.InferRequest,
) (*inferencepb.InferReply, error) {
	server.mu.Lock()
	server.calls[request.GetInput()] = append(
		server.calls[request.GetInput()], guardInferenceCall{
			model:           request.GetModel(),
			reasoningEffort: request.GetGenerationOptions().GetReasoningEffort(),
		},
	)
	server.mu.Unlock()
	decision := string(expectedAllow)
	if guardInferenceBlockCommands[request.GetInput()] {
		decision = string(expectedBlock)
	}
	return &inferencepb.InferReply{
		OutputJson: `{"decision":"` + decision + `"}`,
		Status:     inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
	}, nil
}

func (server *guardInferenceServer) models(command string) []string {
	server.mu.Lock()
	defer server.mu.Unlock()
	models := make([]string, 0, len(server.calls[command]))
	for _, call := range server.calls[command] {
		models = append(models, call.model)
	}
	return models
}

func (server *guardInferenceServer) allCalls() map[string][]guardInferenceCall {
	server.mu.Lock()
	defer server.mu.Unlock()
	calls := make(map[string][]guardInferenceCall, len(server.calls))
	for command, commandCalls := range server.calls {
		calls[command] = append([]guardInferenceCall(nil), commandCalls...)
	}
	return calls
}

type guardIndexedRunner struct {
	indexedRoots []string
}

func (runner guardIndexedRunner) Run(
	_ context.Context,
	command []string,
	_ time.Duration,
	_ []byte,
	_ []string,
) (execconcern.RunResult, error) {
	indexed := false
	if len(command) > 0 {
		target := filepath.Clean(command[len(command)-1])
		for _, root := range runner.indexedRoots {
			root = filepath.Clean(root)
			if target == root || strings.HasPrefix(target, root+string(filepath.Separator)) {
				indexed = true
				break
			}
		}
	}
	output, _ := json.Marshal(map[string]bool{"searchable": indexed})
	return execconcern.RunResult{ExitCode: 0, Stdout: string(output)}, nil
}

func TestComposedGuardConfigMatchesTruthSet(t *testing.T) {
	truthFile, err := os.Open(filepath.Join("testdata", "guard_truth.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = truthFile.Close() }()
	truthCases, err := loadGuardTruthSet(truthFile)
	if err != nil {
		t.Fatal(err)
	}
	inferenceServer := &guardInferenceServer{
		calls: make(map[string][]guardInferenceCall),
	}
	endpoint := startGuardInferenceServer(t, inferenceServer)
	cfg := loadGuardConfig(t, endpoint)

	for _, truthCase := range truthCases {
		t.Run(truthCase.ID, func(t *testing.T) {
			execRuntime := NewExecRuntime(guardIndexedRunner{
				indexedRoots: truthCase.IndexedRoots,
			}, nil)
			ctx := WithExecRuntime(context.Background(), execRuntime)
			inferRuntime := NewInferRuntimeWithCache(nil, nil)
			t.Cleanup(inferRuntime.Close)
			ctx = WithInferRuntime(ctx, inferRuntime)
			if truthCase.GitState != nil {
				state := guardGitState(*truthCase.GitState)
				ctx = WithGitStateReader(ctx, func(string) (gitbranch.State, error) {
					return state, nil
				})
			}
			fields := FieldSet{
				ToolInputCommand: truthCase.Command,
				ToolName:         "Shell",
				CWD:              truthCase.CWD,
			}
			getenv := func(name string) string {
				return truthCase.Environment[name]
			}
			violations := EvaluateAll(
				ctx, "codex", "PreToolUse", fields, cfg.Rules, getenv,
			)
			got := expectedAllow
			if len(violations) > 0 {
				got = expectedBlock
			}
			if got != truthCase.Expected {
				t.Fatalf("decision = %s, want %s; violations = %+v", got, truthCase.Expected, violations)
			}
			models := inferenceServer.models(truthCase.Command)
			if len(models) == 2 && strings.Join(models, ",") != "v4,gpt-5.4-mini" {
				t.Fatalf("inference models = %v", models)
			}
		})
	}
	wantInference := map[string]string{
		`echo /repo/main/go.mod | xargs grep module`: "v4,gpt-5.4-mini",
		`sh -c 'D=/repo/main; grep -rn foo "$D"'`:    "v4,gpt-5.4-mini",
		`eval "$(printf 'grep -rn foo /repo/main')"`: "v4,gpt-5.4-mini",
		`grep module "$(printf /repo/main/go.mod)"`:  "v4,gpt-5.4-mini",
		`bash -c 'rg TODO /repo/main'`:               "v4,gpt-5.4-mini",
		`zsh -c 'git -C /repo/main grep TODO'`:       "v4,gpt-5.4-mini",
		`bash -c 'rg TODO /tmp/project'`:             "v4",
	}
	allCalls := inferenceServer.allCalls()
	if len(allCalls) != len(wantInference) {
		t.Fatalf("inference commands = %v, want only parser-gap cases", allCalls)
	}
	for command, wantModels := range wantInference {
		commandCalls := allCalls[command]
		models := make([]string, 0, len(commandCalls))
		for _, call := range commandCalls {
			models = append(models, call.model)
			if call.model == "gpt-5.4-mini" && call.reasoningEffort != inferencepb.ReasoningEffort_REASONING_EFFORT_HIGH {
				t.Fatalf("mini reasoning effort for %q = %s", command, call.reasoningEffort)
			}
		}
		if gotModels := strings.Join(models, ","); gotModels != wantModels {
			t.Fatalf("inference models for %q = %q, want %q", command, gotModels, wantModels)
		}
	}
}

func startGuardInferenceServer(t *testing.T, service *guardInferenceServer) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	inferencepb.RegisterInferenceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return listener.Addr().String()
}

func loadGuardConfig(t *testing.T, endpoint string) *config.Config {
	t.Helper()
	path := filepath.Join("testdata", "guard_config.toml")
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	temporaryPath := filepath.Join(t.TempDir(), "config.toml")
	content := strings.ReplaceAll(string(bytes), "127.0.0.1:5401", endpoint)
	if err := os.WriteFile(temporaryPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadExisting(temporaryPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func guardGitState(fixture guardGitStateFixture) gitbranch.State {
	worktrees := make([]gitbranch.Worktree, 0, len(fixture.Worktrees))
	for _, worktree := range fixture.Worktrees {
		worktrees = append(worktrees, gitbranch.Worktree{
			Path: worktree.Path, Branch: worktree.Branch, IsPrimary: worktree.IsPrimary,
		})
	}
	return gitbranch.State{
		PrimaryCheckout: fixture.PrimaryCheckout, DefaultBranch: fixture.DefaultBranch,
		CurrentWorktree: fixture.CurrentWorktree, CurrentBranch: fixture.CurrentBranch,
		LocalBranches: nil, Worktrees: worktrees,
	}
}
