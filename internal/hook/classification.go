package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
)

// ClassificationConfidence describes how directly the available evidence
// identifies one provider.
type ClassificationConfidence string

// Classification confidence values.
const (
	ClassificationConfidenceNone   ClassificationConfidence = "none"
	ClassificationConfidenceLow    ClassificationConfidence = "low"
	ClassificationConfidenceMedium ClassificationConfidence = "medium"
	ClassificationConfidenceHigh   ClassificationConfidence = "high"
)

// ClassificationResult describes whether provider resolution succeeded.
type ClassificationResult string

// Classification result values.
const (
	ClassificationResultResolved  ClassificationResult = "resolved"
	ClassificationResultAmbiguous ClassificationResult = "ambiguous"
	ClassificationResultUnknown   ClassificationResult = "unknown"
	ClassificationResultInvalid   ClassificationResult = "invalid"
)

// Classification records the evidence and outcome of provider resolution.
type Classification struct {
	Input            ClassificationInput      `json:"input"`
	ResolvedProvider string                   `json:"resolved_provider"`
	Confidence       ClassificationConfidence `json:"confidence"`
	Evidence         []ClassificationEvidence `json:"evidence"`
	Result           ClassificationResult     `json:"result"`
}

// ResolvedSystem returns the provider selected by the classifier.
func (classification Classification) ResolvedSystem() System {
	return SystemFromString(classification.ResolvedProvider)
}

// ClassificationInput records the exact routing values considered by the
// classifier without duplicating the stored raw payload.
type ClassificationInput struct {
	ProviderHint    string            `json:"provider_hint"`
	Argv            []string          `json:"argv"`
	Environment     map[string]string `json:"environment"`
	RawPayloadBytes int               `json:"raw_payload_bytes"`
	RawPayloadHash  string            `json:"raw_payload_hash"`
}

// ClassificationEvidence records one supplied provider signal.
type ClassificationEvidence struct {
	Source   string `json:"source"`
	Signal   string `json:"signal"`
	Provider string `json:"provider"`
	Strength string `json:"strength"`
	Result   string `json:"result"`
}

type classificationStrength int

const (
	classificationStrengthShared classificationStrength = iota + 1
	classificationStrengthEnvironment
	classificationStrengthPayload
	classificationStrengthRoute
)

type classificationCandidate struct {
	source   string
	signal   string
	provider System
	strength classificationStrength
}

type classificationCommand string

const (
	classificationCommandCodex   classificationCommand = "codex-hook"
	classificationCommandCopilot classificationCommand = "copilot-hook"
	classificationCommandGemini  classificationCommand = "gemini-hook"
)

type environmentSignal struct {
	name     string
	provider System
	strength classificationStrength
	match    func(string) bool
}

var classificationEnvironmentSignals = []environmentSignal{
	{name: "CODEX_THREAD_ID", provider: SystemCodex, strength: classificationStrengthEnvironment, match: nonEmptySignal},
	{name: "CODEX_CI", provider: SystemCodex, strength: classificationStrengthEnvironment, match: nonEmptySignal},
	{name: "COPILOT_OTEL_FILE_EXPORTER_PATH", provider: SystemCopilot, strength: classificationStrengthEnvironment, match: nonEmptySignal},
	{name: "COPILOT_OTEL_ENABLED", provider: SystemCopilot, strength: classificationStrengthEnvironment, match: nonEmptySignal},
	{name: "COPILOT_OTEL_EXPORTER_TYPE", provider: SystemCopilot, strength: classificationStrengthEnvironment, match: nonEmptySignal},
	{name: "CURSOR_VERSION", provider: SystemCursor, strength: classificationStrengthEnvironment, match: nonEmptySignal},
	{name: "CURSOR_WORKSPACE_NAME", provider: SystemCursor, strength: classificationStrengthEnvironment, match: nonEmptySignal},
	{name: "CURSOR_MODE", provider: SystemCursor, strength: classificationStrengthEnvironment, match: nonEmptySignal},
	{name: "GEMINI_CLI", provider: SystemGemini, strength: classificationStrengthEnvironment, match: nonEmptySignal},
	{name: "CLAUDE_CODE_ENTRYPOINT", provider: SystemClaude, strength: classificationStrengthEnvironment, match: nonEmptySignal},
	{name: "AI_AGENT", provider: SystemClaude, strength: classificationStrengthEnvironment, match: claudeAgentSignal},
	{name: "VSCODE_PID", provider: SystemVSCode, strength: classificationStrengthShared, match: nonEmptySignal},
	{name: "VSCODE_IPC_HOOK", provider: SystemVSCode, strength: classificationStrengthShared, match: nonEmptySignal},
}

// Classify resolves a provider from command, payload, event, and environment
// evidence while retaining every supplied signal.
func Classify(
	rawBytes []byte,
	hint System,
	argv []string,
	environment map[string]string,
) Classification {
	input := ClassificationInput{
		ProviderHint:    hint.String(),
		Argv:            slices.Clone(argv),
		Environment:     classificationEnvironment(environment),
		RawPayloadBytes: len(rawBytes),
		RawPayloadHash:  classificationPayloadHash(rawBytes),
	}
	candidates := routeCandidates(hint, argv)

	var payload DetectionPayload
	var parseErr error
	if len(rawBytes) == 0 {
		parseErr = &emptyClassificationInputError{}
	} else {
		payload, parseErr = ParseDetectionPayload(rawBytes)
	}
	if parseErr == nil {
		candidates = append(candidates, payloadCandidates(payload)...)
	}
	candidates = append(candidates, environmentCandidates(input.Environment)...)

	classification := resolveClassification(input, candidates)
	if parseErr != nil {
		classification.Result = ClassificationResultInvalid
	}
	return classification
}

func routeCandidates(hint System, argv []string) []classificationCandidate {
	candidates := make([]classificationCandidate, 0, 2)
	if hint != SystemUnknown {
		candidates = append(candidates, classificationCandidate{
			source: "hint", signal: hint.String(), provider: hint,
			strength: classificationStrengthRoute,
		})
	}
	for _, argument := range argv {
		provider := SystemUnknown
		switch classificationCommand(argument) {
		case classificationCommandCodex:
			provider = SystemCodex
		case classificationCommandCopilot:
			provider = SystemCopilot
		case classificationCommandGemini:
			provider = SystemGemini
		}
		if provider == SystemUnknown {
			continue
		}
		candidates = append(candidates, classificationCandidate{
			source: "command", signal: argument, provider: provider,
			strength: classificationStrengthRoute,
		})
	}
	return candidates
}

func payloadCandidates(payload DetectionPayload) []classificationCandidate {
	candidates := make([]classificationCandidate, 0, 5)
	if hasCursorPayload(payload) {
		candidates = append(candidates, classificationCandidate{
			source: "payload", signal: "cursor fields", provider: SystemCursor,
			strength: classificationStrengthPayload,
		})
	}
	if hasCopilotPayload(payload) {
		candidates = append(candidates, classificationCandidate{
			source: "payload", signal: "copilot fields", provider: SystemCopilot,
			strength: classificationStrengthPayload,
		})
	}
	if hasGeminiEvent(payload) {
		candidates = append(candidates, classificationCandidate{
			source: "event", signal: payload.HookEventName, provider: SystemGemini,
			strength: classificationStrengthPayload,
		})
	}
	if hasClaudePayload(payload) {
		candidates = append(candidates, classificationCandidate{
			source: "payload", signal: "claude-compatible fields", provider: SystemClaude,
			strength: classificationStrengthShared,
		})
	}
	if hasCursorEvent(payload) {
		candidates = append(candidates,
			classificationCandidate{
				source: "event", signal: payload.HookEventName, provider: SystemCursor,
				strength: classificationStrengthShared,
			},
			classificationCandidate{
				source: "event", signal: payload.HookEventName, provider: SystemCopilot,
				strength: classificationStrengthShared,
			},
		)
	}
	return candidates
}

func environmentCandidates(environment map[string]string) []classificationCandidate {
	candidates := make([]classificationCandidate, 0, len(environment))
	for _, signal := range classificationEnvironmentSignals {
		value := environment[signal.name]
		if !signal.match(value) {
			continue
		}
		candidates = append(candidates, classificationCandidate{
			source: "environment", signal: signal.name, provider: signal.provider,
			strength: signal.strength,
		})
	}
	return candidates
}

func resolveClassification(
	input ClassificationInput,
	candidates []classificationCandidate,
) Classification {
	maxStrength := classificationStrength(0)
	providers := make(map[System]bool)
	for _, candidate := range candidates {
		if candidate.strength > maxStrength {
			maxStrength = candidate.strength
			clear(providers)
		}
		if candidate.strength == maxStrength {
			providers[candidate.provider] = true
		}
	}

	resolved := SystemUnknown
	result := ClassificationResultUnknown
	confidence := ClassificationConfidenceNone
	if len(providers) == 1 {
		for provider := range providers {
			resolved = provider
		}
		result = ClassificationResultResolved
		confidence = classificationConfidence(maxStrength, resolved, candidates)
	} else if len(providers) > 1 {
		result = ClassificationResultAmbiguous
	}

	evidence := make([]ClassificationEvidence, 0, len(candidates))
	for _, candidate := range candidates {
		evidence = append(evidence, ClassificationEvidence{
			Source:   candidate.source,
			Signal:   candidate.signal,
			Provider: candidate.provider.String(),
			Strength: candidate.strength.String(),
			Result:   evidenceResult(candidate.provider, resolved, result),
		})
	}
	return Classification{
		Input: input, ResolvedProvider: resolved.String(), Confidence: confidence,
		Evidence: evidence, Result: result,
	}
}

func classificationConfidence(
	strength classificationStrength,
	resolved System,
	candidates []classificationCandidate,
) ClassificationConfidence {
	if strength < classificationStrengthPayload {
		return ClassificationConfidenceLow
	}
	for _, candidate := range candidates {
		if candidate.provider != resolved && candidate.strength >= classificationStrengthPayload {
			return ClassificationConfidenceMedium
		}
	}
	return ClassificationConfidenceHigh
}

func evidenceResult(
	provider System,
	resolved System,
	result ClassificationResult,
) string {
	if result != ClassificationResultResolved {
		return "candidate"
	}
	if provider == resolved {
		return "match"
	}
	return "conflict"
}

func (strength classificationStrength) String() string {
	switch strength {
	case classificationStrengthShared:
		return "shared"
	case classificationStrengthEnvironment:
		return "environment"
	case classificationStrengthPayload:
		return "payload"
	case classificationStrengthRoute:
		return "route"
	default:
		return "none"
	}
}

func classificationEnvironment(environment map[string]string) map[string]string {
	result := make(map[string]string)
	for _, signal := range classificationEnvironmentSignals {
		value := environment[signal.name]
		if value != "" {
			result[signal.name] = value
		}
	}
	return result
}

func classificationPayloadHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func nonEmptySignal(value string) bool {
	return value != ""
}

func claudeAgentSignal(value string) bool {
	return strings.HasPrefix(value, "claude-code/")
}

type emptyClassificationInputError struct{}

func (*emptyClassificationInputError) Error() string {
	return "empty classification input"
}
