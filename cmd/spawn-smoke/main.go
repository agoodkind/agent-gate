package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	defaultProbeUpperBound = 8 * 1024 * 1024
	defaultProbeStep       = 64 * 1024
	defaultPayloadKind     = "beforeSubmitPrompt"
	defaultGenerateBytes   = 0
)

type payloadKind string

const (
	payloadKindBeforeReadFile     payloadKind = "beforeReadFile"
	payloadKindBeforeSubmitPrompt payloadKind = "beforeSubmitPrompt"
	payloadKindPreToolUse         payloadKind = "preToolUse"
)

type config struct {
	targetBinary    string
	inputFile       string
	generateBytes   int
	payloadKind     string
	argvPadBytes    int
	envPadBytes     int
	probeEnv        bool
	probeArgv       bool
	probeCombined   bool
	probeUpperBound int
	probeStep       int
}

type report struct {
	TargetBinary string        `json:"target_binary"`
	InputFile    string        `json:"input_file"`
	InputMode    string        `json:"input_mode"`
	InputBytes   int           `json:"input_bytes"`
	PayloadKind  string        `json:"payload_kind"`
	PayloadBytes int           `json:"payload_bytes"`
	Baseline     runResult     `json:"baseline"`
	Probes       []probeReport `json:"probes"`
}

type probeReport struct {
	Name      string    `json:"name"`
	LowOK     int       `json:"low_ok_bytes"`
	HighFail  int       `json:"high_fail_bytes"`
	LastOK    runResult `json:"last_ok"`
	FirstFail runResult `json:"first_fail"`
}

type runResult struct {
	OK              bool   `json:"ok"`
	ArgvPadBytes    int    `json:"argv_pad_bytes"`
	EnvPadBytes     int    `json:"env_pad_bytes"`
	ElapsedMS       int64  `json:"elapsed_ms"`
	StdoutBytes     int    `json:"stdout_bytes"`
	StderrBytes     int    `json:"stderr_bytes"`
	ExitCode        int    `json:"exit_code"`
	ErrorKind       string `json:"error_kind"`
	ErrorMessage    string `json:"error_message"`
	ResponseSnippet string `json:"response_snippet"`
}

type padScenario struct {
	argvPadBytes int
	envPadBytes  int
}

type smokePayload struct {
	ConversationID string              `json:"conversation_id"`
	GenerationID   string              `json:"generation_id"`
	SessionID      string              `json:"session_id"`
	Model          string              `json:"model"`
	CursorVersion  string              `json:"cursor_version"`
	CWD            string              `json:"cwd"`
	WorkspaceRoots []string            `json:"workspace_roots"`
	Attachments    []map[string]string `json:"attachments"`
	UserEmail      string              `json:"user_email"`
	TranscriptPath string              `json:"transcript_path"`
	HookEventName  string              `json:"hook_event_name"`
	Prompt         string              `json:"prompt,omitempty"`
	FilePath       string              `json:"file_path,omitempty"`
	Content        string              `json:"content,omitempty"`
	ToolName       string              `json:"tool_name,omitempty"`
	ToolInput      *smokeToolInput     `json:"tool_input,omitempty"`
}

type smokeToolInput struct {
	FilePath  string `json:"file_path"`
	Content   string `json:"content"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	logger.Debug("spawn smoke run starting")
	os.Exit(run(logger))
}

func run(logger *slog.Logger) int {
	cfg, err := parseFlags()
	if err != nil {
		logger.Error("invalid spawn smoke flags", slog.Any("err", err))
		return 2
	}

	inputFilePath, inputMode, inputBytes, cleanup, err := loadInput(cfg, logger)
	if err != nil {
		logger.Error("load spawn smoke input failed", slog.Any("err", err))
		return 2
	}
	defer cleanup()

	targetBinary, err := resolveTargetBinary(cfg.targetBinary, logger)
	if err != nil {
		logger.Error("resolve spawn smoke target failed", slog.Any("err", err))
		return 2
	}

	payloadBytes, err := buildPayload(cfg.payloadKind, string(inputBytes), logger)
	if err != nil {
		logger.Error("build spawn smoke payload failed", slog.Any("err", err))
		return 2
	}

	out := report{
		TargetBinary: targetBinary,
		InputFile:    inputFilePath,
		InputMode:    inputMode,
		InputBytes:   len(inputBytes),
		PayloadKind:  cfg.payloadKind,
		PayloadBytes: len(payloadBytes),
		Baseline: runScenario(targetBinary, payloadBytes, padScenario{
			argvPadBytes: cfg.argvPadBytes,
			envPadBytes:  cfg.envPadBytes,
		}),
	}

	if cfg.probeEnv {
		out.Probes = append(out.Probes, probeLimit(
			"env_only",
			targetBinary,
			payloadBytes,
			cfg.probeStep,
			cfg.probeUpperBound,
			func(size int) padScenario {
				return padScenario{argvPadBytes: cfg.argvPadBytes, envPadBytes: size}
			},
		))
	}

	if cfg.probeArgv {
		out.Probes = append(out.Probes, probeLimit(
			"argv_only",
			targetBinary,
			payloadBytes,
			cfg.probeStep,
			cfg.probeUpperBound,
			func(size int) padScenario {
				return padScenario{argvPadBytes: size, envPadBytes: cfg.envPadBytes}
			},
		))
	}

	if cfg.probeCombined {
		out.Probes = append(out.Probes, probeLimit(
			"argv_plus_env_equal_split",
			targetBinary,
			payloadBytes,
			cfg.probeStep,
			cfg.probeUpperBound,
			func(size int) padScenario {
				argvPadBytes := size / 2
				envPadBytes := size - argvPadBytes
				return padScenario{argvPadBytes: argvPadBytes, envPadBytes: envPadBytes}
			},
		))
	}

	logger.Info("spawn smoke run completed",
		slog.String("target_binary", out.TargetBinary),
		slog.String("input_mode", out.InputMode),
		slog.Int("input_bytes", out.InputBytes),
		slog.String("payload_kind", out.PayloadKind),
		slog.Int("payload_bytes", out.PayloadBytes),
		slog.Bool("baseline_ok", out.Baseline.OK),
		slog.Int("probe_count", len(out.Probes)),
	)

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(out); err != nil {
		logger.Error("encode spawn smoke report failed", slog.Any("err", err))
		return 2
	}

	return 0
}

func parseFlags() (config, error) {
	cfg := config{}

	defaultTarget := filepath.Join(os.Getenv("HOME"), ".local", "bin", "agent-gate")

	flag.StringVar(&cfg.targetBinary, "target", defaultTarget, "path to the agent-gate binary to execute")
	flag.StringVar(&cfg.inputFile, "input-file", "", "path to a large text file used to build the hook payload")
	flag.IntVar(&cfg.generateBytes, "generate-bytes", defaultGenerateBytes, "generate lorem-style input of this size in a temporary directory instead of reading -input-file")
	flag.StringVar(&cfg.payloadKind, "payload-kind", defaultPayloadKind, "payload shape: beforeSubmitPrompt, beforeReadFile, or preToolUse")
	flag.IntVar(&cfg.argvPadBytes, "argv-pad-bytes", 0, "fixed argv padding to add to every spawn")
	flag.IntVar(&cfg.envPadBytes, "env-pad-bytes", 0, "fixed env padding to add to every spawn")
	flag.BoolVar(&cfg.probeEnv, "probe-env", true, "binary-search the env padding failure threshold")
	flag.BoolVar(&cfg.probeArgv, "probe-argv", true, "binary-search the argv padding failure threshold")
	flag.BoolVar(&cfg.probeCombined, "probe-combined", true, "binary-search the combined argv+env failure threshold")
	flag.IntVar(&cfg.probeUpperBound, "probe-upper-bound", defaultProbeUpperBound, "maximum synthetic padding to test in bytes")
	flag.IntVar(&cfg.probeStep, "probe-step", defaultProbeStep, "search granularity in bytes")
	flag.Parse()

	if cfg.inputFile == "" && cfg.generateBytes <= 0 {
		return config{}, errors.New("missing input source: set -input-file or -generate-bytes")
	}
	if cfg.inputFile != "" && cfg.generateBytes > 0 {
		return config{}, errors.New("choose only one input source: -input-file or -generate-bytes")
	}
	if cfg.generateBytes < 0 {
		return config{}, errors.New("-generate-bytes must be zero or positive")
	}
	if cfg.probeUpperBound <= 0 {
		return config{}, errors.New("-probe-upper-bound must be positive")
	}
	if cfg.probeStep <= 0 {
		return config{}, errors.New("-probe-step must be positive")
	}

	return cfg, nil
}

func loadInput(cfg config, logger *slog.Logger) (string, string, []byte, func(), error) {
	if cfg.inputFile != "" {
		inputBytes, err := os.ReadFile(cfg.inputFile)
		if err != nil {
			logger.Error("read spawn smoke input file failed",
				slog.String("input_file", cfg.inputFile),
				slog.Any("err", err),
			)
			return "", "", nil, func() {}, fmt.Errorf("read input file: %w", err)
		}
		return cfg.inputFile, "file", inputBytes, func() {}, nil
	}

	tempDir, err := os.MkdirTemp("", "agent-gate-spawn-smoke-")
	if err != nil {
		logger.Error("create spawn smoke temp dir failed", slog.Any("err", err))
		return "", "", nil, func() {}, fmt.Errorf("create temp dir: %w", err)
	}

	inputPath := filepath.Join(tempDir, "lorem.txt")
	inputBytes := []byte(generateLoremText(cfg.generateBytes))
	if err := os.WriteFile(inputPath, inputBytes, 0o600); err != nil {
		_ = os.RemoveAll(tempDir)
		logger.Error("write generated spawn smoke input failed",
			slog.String("input_file", inputPath),
			slog.Int("generate_bytes", cfg.generateBytes),
			slog.Any("err", err),
		)
		return "", "", nil, func() {}, fmt.Errorf("write generated input file: %w", err)
	}

	cleanup := func() {
		_ = os.RemoveAll(tempDir)
	}

	return inputPath, "generated", inputBytes, cleanup, nil
}

func resolveTargetBinary(target string, logger *slog.Logger) (string, error) {
	if target == "" {
		return "", errors.New("target binary path is empty")
	}

	info, err := os.Stat(target)
	if err != nil {
		logger.Error("stat spawn smoke target binary failed",
			slog.String("target_binary", target),
			slog.Any("err", err),
		)
		return "", fmt.Errorf("stat target binary %q: %w", target, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("target binary %q is a directory", target)
	}

	return target, nil
}

func buildPayload(payloadKindValue string, largeText string, logger *slog.Logger) ([]byte, error) {
	payload := smokePayload{
		ConversationID: "spawn-smoke-conversation",
		GenerationID:   "spawn-smoke-generation",
		SessionID:      "spawn-smoke-conversation",
		Model:          "gpt-5.4",
		CursorVersion:  "3.2.11",
		CWD:            "/Users/agoodkind/Sites/agent-gate",
		WorkspaceRoots: []string{"/Users/agoodkind/Sites/agent-gate"},
		Attachments:    []map[string]string{},
		UserEmail:      "redacted@example.com",
		TranscriptPath: filepath.Join(os.TempDir(), "spawn-smoke.jsonl"),
	}

	switch payloadKind(payloadKindValue) {
	case payloadKindBeforeSubmitPrompt:
		payload.HookEventName = string(payloadKindBeforeSubmitPrompt)
		payload.Prompt = largeText
	case payloadKindBeforeReadFile:
		payload.HookEventName = string(payloadKindBeforeReadFile)
		payload.FilePath = "/tmp/spawn-smoke.txt"
		payload.Content = largeText
	case payloadKindPreToolUse:
		payload.HookEventName = string(payloadKindPreToolUse)
		payload.ToolName = "edit_file"
		payload.ToolInput = &smokeToolInput{
			FilePath:  "/tmp/spawn-smoke.txt",
			Content:   largeText,
			OldString: "",
			NewString: largeText,
		}
	default:
		return nil, fmt.Errorf("unsupported payload kind %q", payloadKindValue)
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		logger.Error("marshal spawn smoke payload failed",
			slog.String("payload_kind", payloadKindValue),
			slog.Any("err", err),
		)
		return nil, fmt.Errorf("marshal %s payload: %w", payloadKindValue, err)
	}

	return payloadBytes, nil
}

func probeLimit(
	name string,
	targetBinary string,
	payloadBytes []byte,
	step int,
	upperBound int,
	buildScenario func(size int) padScenario,
) probeReport {
	low := 0
	lastOK := runScenario(targetBinary, payloadBytes, buildScenario(low))

	if !lastOK.OK {
		return probeReport{
			Name:      name,
			LowOK:     -1,
			HighFail:  0,
			LastOK:    lastOK,
			FirstFail: lastOK,
		}
	}

	high := step
	firstFail := runScenario(targetBinary, payloadBytes, buildScenario(high))
	for firstFail.OK && high < upperBound {
		low = high
		lastOK = firstFail
		high *= 2
		if high > upperBound {
			high = upperBound
		}
		firstFail = runScenario(targetBinary, payloadBytes, buildScenario(high))
		if high == upperBound {
			break
		}
	}

	if firstFail.OK {
		return probeReport{
			Name:     name,
			LowOK:    high,
			HighFail: -1,
			LastOK:   firstFail,
			FirstFail: runResult{
				OK:           false,
				ErrorKind:    "not_found",
				ErrorMessage: "no failing threshold found within probe upper bound",
			},
		}
	}

	for high-low > step {
		mid := roundUpToStep((low+high)/2, step)
		if mid >= high {
			mid = high - step
		}
		if mid <= low {
			mid = low + step
		}

		candidate := runScenario(targetBinary, payloadBytes, buildScenario(mid))
		if candidate.OK {
			low = mid
			lastOK = candidate
			continue
		}

		high = mid
		firstFail = candidate
	}

	return probeReport{
		Name:      name,
		LowOK:     low,
		HighFail:  high,
		LastOK:    lastOK,
		FirstFail: firstFail,
	}
}

func runScenario(targetBinary string, payloadBytes []byte, scenario padScenario) runResult {
	started := scenarioStartTime()

	args := []string{}
	if scenario.argvPadBytes > 0 {
		args = append(args, "--spawn-smoke-pad="+strings.Repeat("a", scenario.argvPadBytes))
	}

	cmd := exec.CommandContext(context.Background(), targetBinary, args...)
	cmd.Stdin = bytes.NewReader(payloadBytes)
	cmd.Env = append(os.Environ(), "AGENT_GATE_SPAWN_SMOKE_PAD="+strings.Repeat("e", scenario.envPadBytes))

	stdout, err := cmd.Output()

	result := runResult{
		OK:           err == nil,
		ArgvPadBytes: scenario.argvPadBytes,
		EnvPadBytes:  scenario.envPadBytes,
		ElapsedMS:    time.Since(started).Milliseconds(),
		StdoutBytes:  len(stdout),
	}

	if err == nil {
		result.ExitCode = 0
		result.ResponseSnippet = trimSnippet(stdout)
		return result
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		result.StderrBytes = len(exitErr.Stderr)
		result.ErrorKind = "exit_error"
		result.ErrorMessage = exitErr.Error()
		result.ResponseSnippet = trimSnippet(stdout)
		return result
	}

	result.ExitCode = -1
	result.ErrorMessage = err.Error()

	if isE2BIG(err) {
		result.ErrorKind = "e2big"
		return result
	}

	result.ErrorKind = "start_error"
	return result
}

func scenarioStartTime() time.Time {
	return time.Now()
}

func isE2BIG(err error) bool {
	if errors.Is(err, syscall.E2BIG) {
		return true
	}

	return strings.Contains(strings.ToLower(err.Error()), "argument list too long")
}

func roundUpToStep(value int, step int) int {
	remainder := value % step
	if remainder == 0 {
		return value
	}

	return value + step - remainder
}

func trimSnippet(data []byte) string {
	const limit = 160

	if len(data) <= limit {
		return string(data)
	}

	return string(data[:limit]) + "..."
}

func generateLoremText(targetBytes int) string {
	if targetBytes <= 0 {
		return ""
	}

	const paragraph = "Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat. Duis aute irure dolor in reprehenderit in voluptate velit esse cillum dolore eu fugiat nulla pariatur. Excepteur sint occaecat cupidatat non proident, sunt in culpa qui officia deserunt mollit anim id est laborum.\n\n"

	var builder strings.Builder
	builder.Grow(targetBytes + len(paragraph))

	for builder.Len() < targetBytes {
		builder.WriteString(paragraph)
	}

	text := builder.String()
	if len(text) <= targetBytes {
		return text
	}

	return text[:targetBytes]
}
