package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ClassificationConfidence describes how directly the available evidence identifies one provider.
type ClassificationConfidence string

const (
	// ClassificationConfidenceNone means no provider decision was possible.
	ClassificationConfidenceNone ClassificationConfidence = "none"
	// ClassificationConfidenceLow means context alone selected the provider.
	ClassificationConfidenceLow ClassificationConfidence = "low"
	// ClassificationConfidenceMedium means stronger evidence won over a payload conflict.
	ClassificationConfidenceMedium ClassificationConfidence = "medium"
	// ClassificationConfidenceHigh means no payload-strength evidence conflicted.
	ClassificationConfidenceHigh ClassificationConfidence = "high"
)

// ClassificationResult describes whether provider resolution succeeded.
type ClassificationResult string

const (
	// ClassificationResultResolved means one provider won the evidence comparison.
	ClassificationResultResolved ClassificationResult = "resolved"
	// ClassificationResultAmbiguous means multiple providers had equal strongest evidence.
	ClassificationResultAmbiguous ClassificationResult = "ambiguous"
	// ClassificationResultUnknown means no evidence identified a provider.
	ClassificationResultUnknown ClassificationResult = "unknown"
	// ClassificationResultInvalid means the hook payload was empty or unreadable.
	ClassificationResultInvalid ClassificationResult = "invalid"
)

// Classification records the evidence and outcome of provider resolution.
type Classification struct {
	Input            ClassificationInput      `json:"input"`
	ResolvedProvider string                   `json:"resolved_provider"`
	Confidence       ClassificationConfidence `json:"confidence"`
	Evidence         []ClassificationEvidence `json:"evidence"`
	Conflicts        []ClassificationEvidence `json:"conflicts"`
	Result           ClassificationResult     `json:"result"`
}

// ResolvedSystem returns the provider selected by the classifier.
func (classification Classification) ResolvedSystem() System {
	return SystemFromString(classification.ResolvedProvider)
}

// ClassificationInput records every supplied routing value without duplicating raw bytes.
type ClassificationInput struct {
	ProviderHint    string                     `json:"provider_hint"`
	Argv            []string                   `json:"argv"`
	Environment     map[string]string          `json:"environment"`
	Invocation      InvocationContext          `json:"invocation"`
	Payload         PayloadClassificationInput `json:"payload"`
	RawPayloadBytes int                        `json:"raw_payload_bytes"`
	RawPayloadHash  string                     `json:"raw_payload_hash"`
}

// PayloadClassificationInput records the payload surface used by classification.
type PayloadClassificationInput struct {
	Status              SignalStatus                `json:"status"`
	EventName           string                      `json:"event_name"`
	Fields              []PayloadFieldEvidence      `json:"fields"`
	Shapes              []string                    `json:"shapes"`
	ProviderIdentifiers []PayloadProviderIdentifier `json:"provider_identifiers"`
	Error               string                      `json:"error"`
}

// PayloadFieldEvidence records one top-level field and its observed casing.
type PayloadFieldEvidence struct {
	Name   string `json:"name"`
	Casing string `json:"casing"`
}

// PayloadProviderIdentifier records an explicit provider-like payload field.
type PayloadProviderIdentifier struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// ClassificationEvidence records one provider candidate and its provenance.
type ClassificationEvidence struct {
	Source     string `json:"source"`
	Provenance string `json:"provenance"`
	Signal     string `json:"signal"`
	Provider   string `json:"provider"`
	Strength   string `json:"strength"`
	Result     string `json:"result"`
}

type classificationStrength int

const (
	classificationStrengthShared classificationStrength = iota + 1
	classificationStrengthContext
	classificationStrengthRegistration
	classificationStrengthPayload
	classificationStrengthIdentifier
	classificationStrengthRoute
)

type classificationCandidate struct {
	source     string
	provenance string
	signal     string
	provider   System
	strength   classificationStrength
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
	{name: "CODEX_THREAD_ID", provider: SystemCodex, strength: classificationStrengthContext, match: nonEmptySignal},
	{name: "CODEX_CI", provider: SystemCodex, strength: classificationStrengthContext, match: nonEmptySignal},
	{name: "COPILOT_OTEL_FILE_EXPORTER_PATH", provider: SystemCopilot, strength: classificationStrengthContext, match: nonEmptySignal},
	{name: "COPILOT_OTEL_ENABLED", provider: SystemCopilot, strength: classificationStrengthContext, match: nonEmptySignal},
	{name: "COPILOT_OTEL_EXPORTER_TYPE", provider: SystemCopilot, strength: classificationStrengthContext, match: nonEmptySignal},
	{name: "CURSOR_VERSION", provider: SystemCursor, strength: classificationStrengthContext, match: nonEmptySignal},
	{name: "CURSOR_WORKSPACE_NAME", provider: SystemCursor, strength: classificationStrengthContext, match: nonEmptySignal},
	{name: "CURSOR_MODE", provider: SystemCursor, strength: classificationStrengthContext, match: nonEmptySignal},
	{name: "GEMINI_CLI", provider: SystemGemini, strength: classificationStrengthContext, match: nonEmptySignal},
	{name: "CLAUDE_CODE_ENTRYPOINT", provider: SystemClaude, strength: classificationStrengthContext, match: nonEmptySignal},
	{name: "AI_AGENT", provider: SystemClaude, strength: classificationStrengthContext, match: claudeAgentSignal},
	{name: "VSCODE_PID", provider: SystemVSCode, strength: classificationStrengthShared, match: nonEmptySignal},
	{name: "VSCODE_IPC_HOOK", provider: SystemVSCode, strength: classificationStrengthShared, match: nonEmptySignal},
}

var payloadProviderIdentifierFields = map[string]bool{
	"provider": true, "provider_id": true, "providerId": true,
	"system": true, "client": true, "client_name": true, "clientName": true,
}

// Classify resolves a provider from legacy request evidence.
func Classify(
	rawBytes []byte,
	hint System,
	argv []string,
	environment map[string]string,
) Classification {
	return ClassifyWithContext(rawBytes, hint.String(), argv, environment, InvocationContext{
		HookSubcommand: ObservedValue{
			Value: "", Source: "", Provenance: "", Status: "",
		},
		HookTags: nil,
		WorkingDirectory: ObservedValue{
			Value: "", Source: "", Provenance: "", Status: "",
		},
		Executable: ProcessEvidence{
			Name: "", ExecutablePath: "", Source: "", Provenance: "", Status: "",
		},
		ParentProcess: ProcessEvidence{
			Name: "", ExecutablePath: "", Source: "", Provenance: "", Status: "",
		},
		Ancestors:   nil,
		Environment: nil,
		ManagedRegistration: ObservedValue{
			Value: "", Source: "", Provenance: "", Status: "",
		},
		CollectionIssues: nil,
	})
}

// ClassifyWithContext resolves a provider from the complete invocation evidence set.
func ClassifyWithContext(
	rawBytes []byte,
	providerHint string,
	argv []string,
	environment map[string]string,
	invocation InvocationContext,
) Classification {
	invocation = completeInvocationEnvironment(invocation, environment)
	payloadInput, payload, parseErr := unmarshalClassificationPayload(rawBytes)
	input := ClassificationInput{
		ProviderHint:    providerHint,
		Argv:            slices.Clone(argv),
		Environment:     classificationEnvironment(environment),
		Invocation:      invocation,
		Payload:         payloadInput,
		RawPayloadBytes: len(rawBytes),
		RawPayloadHash:  classificationPayloadHash(rawBytes),
	}

	candidates := routeCandidates(providerHint, argv)
	candidates = append(candidates, invocationCandidates(invocation)...)
	if parseErr == nil {
		candidates = append(candidates, payloadCandidates(payload, payloadInput)...)
	}
	candidates = append(candidates, environmentCandidates(invocation.Environment)...)

	classification := resolveClassification(input, candidates)
	if parseErr != nil {
		classification.ResolvedProvider = SystemUnknown.String()
		classification.Result = ClassificationResultInvalid
		classification.Confidence = ClassificationConfidenceNone
		classification.Conflicts = nil
		for i := range classification.Evidence {
			classification.Evidence[i].Result = "candidate"
		}
	}
	return classification
}

func routeCandidates(providerHint string, argv []string) []classificationCandidate {
	candidates := make([]classificationCandidate, 0, 2)
	if provider := providerFromSignal(providerHint); provider != SystemUnknown {
		candidates = append(candidates, classificationCandidate{
			source: "provider_hint", provenance: "request", signal: providerHint,
			provider: provider, strength: classificationStrengthRoute,
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
			source: "hook_subcommand", provenance: "literal_argv", signal: argument,
			provider: provider, strength: classificationStrengthRoute,
		})
	}
	return candidates
}

func invocationCandidates(invocation InvocationContext) []classificationCandidate {
	candidates := make([]classificationCandidate, 0, len(invocation.Ancestors)+2)
	registration := invocation.ManagedRegistration
	if registration.Status == SignalStatusObserved {
		if provider := providerFromSignal(registration.Value); provider != SystemUnknown {
			candidates = append(candidates, classificationCandidate{
				source: registration.Source, provenance: registration.Provenance,
				signal: registration.Value, provider: provider,
				strength: classificationStrengthRegistration,
			})
		}
	}
	processes := make([]ProcessEvidence, 0, len(invocation.Ancestors)+2)
	processes = append(processes, invocation.Executable, invocation.ParentProcess)
	processes = append(processes, invocation.Ancestors...)
	for _, process := range processes {
		if process.Status != SignalStatusObserved {
			continue
		}
		provider := providerFromProcess(process)
		if provider == SystemUnknown {
			continue
		}
		candidates = append(candidates, classificationCandidate{
			source: process.Source, provenance: process.Provenance, signal: process.Name,
			provider: provider, strength: classificationStrengthContext,
		})
	}
	return candidates
}

func payloadCandidates(
	payload DetectionPayload,
	input PayloadClassificationInput,
) []classificationCandidate {
	candidates := make([]classificationCandidate, 0, len(input.Shapes)+len(input.ProviderIdentifiers))
	for _, identifier := range input.ProviderIdentifiers {
		provider := providerFromSignal(identifier.Value)
		if provider == SystemUnknown {
			continue
		}
		candidates = append(candidates, classificationCandidate{
			source: "payload_identifier", provenance: identifier.Field,
			signal: identifier.Value, provider: provider,
			strength: classificationStrengthIdentifier,
		})
	}
	if hasCursorPayload(payload) {
		candidates = append(candidates, payloadCandidate("cursor_fields", SystemCursor, classificationStrengthPayload))
	}
	if hasCopilotPayload(payload) {
		candidates = append(candidates, payloadCandidate("copilot_fields", SystemCopilot, classificationStrengthPayload))
	}
	if hasGeminiEvent(payload) {
		candidates = append(candidates, classificationCandidate{
			source: "payload_event", provenance: "hook_event_name", signal: payload.HookEventName,
			provider: SystemGemini, strength: classificationStrengthPayload,
		})
	}
	if hasClaudePayload(payload) {
		candidates = append(candidates, payloadCandidate("claude_compatible_fields", SystemClaude, classificationStrengthShared))
	}
	if hasCursorEvent(payload) {
		candidates = append(candidates,
			classificationCandidate{
				source: "payload_event", provenance: "hook_event_name", signal: payload.HookEventName,
				provider: SystemCursor, strength: classificationStrengthShared,
			},
			classificationCandidate{
				source: "payload_event", provenance: "hook_event_name", signal: payload.HookEventName,
				provider: SystemCopilot, strength: classificationStrengthShared,
			},
		)
	}
	return candidates
}

func payloadCandidate(
	signal string,
	provider System,
	strength classificationStrength,
) classificationCandidate {
	return classificationCandidate{
		source: "payload_shape", provenance: "raw_payload", signal: signal,
		provider: provider, strength: strength,
	}
}

func environmentCandidates(environment []EnvironmentEvidence) []classificationCandidate {
	candidates := make([]classificationCandidate, 0, len(environment))
	for _, evidence := range environment {
		if evidence.Status != SignalStatusObserved {
			continue
		}
		if evidence.Name == "AGENT_GATE_HOOK_PROVIDER" ||
			evidence.Name == "AGENT_GATE_HOOK_REGISTRATION" {
			provider := providerFromSignal(evidence.Value)
			if provider != SystemUnknown {
				candidates = append(candidates, classificationCandidate{
					source: evidence.Source, provenance: evidence.Provenance,
					signal: evidence.Name, provider: provider,
					strength: classificationStrengthContext,
				})
			}
			continue
		}
		for _, signal := range classificationEnvironmentSignals {
			if signal.name != evidence.Name || !signal.match(evidence.Value) {
				continue
			}
			candidates = append(candidates, classificationCandidate{
				source: evidence.Source, provenance: evidence.Provenance,
				signal: signal.name, provider: signal.provider, strength: signal.strength,
			})
		}
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
	conflicts := make([]ClassificationEvidence, 0)
	for _, candidate := range candidates {
		item := ClassificationEvidence{
			Source: candidate.source, Provenance: candidate.provenance,
			Signal: candidate.signal, Provider: candidate.provider.String(),
			Strength: candidate.strength.String(),
			Result:   evidenceResult(candidate.provider, resolved, result),
		}
		evidence = append(evidence, item)
		if item.Result == "conflict" {
			conflicts = append(conflicts, item)
		}
	}
	return Classification{
		Input: input, ResolvedProvider: resolved.String(), Confidence: confidence,
		Evidence: evidence, Conflicts: conflicts, Result: result,
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

func evidenceResult(provider System, resolved System, result ClassificationResult) string {
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
	case classificationStrengthContext:
		return "context"
	case classificationStrengthRegistration:
		return "registration"
	case classificationStrengthPayload:
		return "payload"
	case classificationStrengthIdentifier:
		return "identifier"
	case classificationStrengthRoute:
		return "route"
	default:
		return "none"
	}
}

func completeInvocationEnvironment(
	invocation InvocationContext,
	environment map[string]string,
) InvocationContext {
	if len(invocation.Environment) > 0 {
		return invocation
	}
	filtered := classificationEnvironment(environment)
	names := make([]string, 0, len(filtered))
	for name := range filtered {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		invocation.Environment = append(invocation.Environment, EnvironmentEvidence{
			Name: name, Value: filtered[name], Category: "provider_environment",
			Source: "environment", Provenance: "inherited_environment",
			Status: SignalStatusObserved,
		})
	}
	return invocation
}

func classificationEnvironment(environment map[string]string) map[string]string {
	result := make(map[string]string)
	for _, signal := range classificationEnvironmentSignals {
		value := environment[signal.name]
		if value != "" {
			result[signal.name] = value
		}
	}
	for _, name := range []string{"AGENT_GATE_HOOK_PROVIDER", "AGENT_GATE_HOOK_REGISTRATION", "AGENT_GATE_HOOK_EVENT"} {
		if value := environment[name]; value != "" {
			result[name] = value
		}
	}
	return result
}

func unmarshalClassificationPayload(
	rawBytes []byte,
) (PayloadClassificationInput, DetectionPayload, error) {
	input := PayloadClassificationInput{
		Status: SignalStatusObserved, EventName: "", Fields: nil, Shapes: nil,
		ProviderIdentifiers: nil, Error: "",
	}
	var payload DetectionPayload
	if len(rawBytes) == 0 {
		err := &emptyClassificationInputError{}
		input.Status = SignalStatusMissing
		input.Error = err.Error()
		return input, payload, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawBytes, &fields); err != nil {
		input.Status = SignalStatusUnreadable
		input.Error = "payload is not valid JSON"
		slog.Warn("decode classification fields failed", slog.Any("err", err))
		return input, payload, fmt.Errorf("decode classification fields: %w", err)
	}
	if fields == nil {
		err := &nonObjectClassificationInputError{}
		input.Status = SignalStatusUnreadable
		input.Error = err.Error()
		return input, payload, err
	}
	if err := json.Unmarshal(rawBytes, &payload); err != nil {
		input.Status = SignalStatusUnreadable
		input.Error = "payload shape is unreadable"
		slog.Warn("decode classification payload failed", slog.Any("err", err))
		return input, payload, fmt.Errorf("decode classification payload: %w", err)
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		input.Fields = append(input.Fields, PayloadFieldEvidence{
			Name: name, Casing: classificationFieldCasing(name),
		})
		if !payloadProviderIdentifierFields[name] {
			continue
		}
		var value string
		if err := json.Unmarshal(fields[name], &value); err != nil {
			continue
		}
		input.ProviderIdentifiers = append(input.ProviderIdentifiers,
			PayloadProviderIdentifier{Field: name, Value: value})
	}
	input.EventName = payload.HookEventName
	input.Shapes = detectedPayloadShapes(payload)
	return input, payload, nil
}

func detectedPayloadShapes(payload DetectionPayload) []string {
	shapes := make([]string, 0, 5)
	if hasCursorPayload(payload) {
		shapes = append(shapes, "cursor_fields")
	}
	if hasCopilotPayload(payload) {
		shapes = append(shapes, "copilot_fields")
	}
	if hasGeminiEvent(payload) {
		shapes = append(shapes, "gemini_event")
	}
	if hasClaudePayload(payload) {
		shapes = append(shapes, "claude_compatible_fields")
	}
	if hasCursorEvent(payload) {
		shapes = append(shapes, "shared_lowercase_event")
	}
	return shapes
}

func classificationFieldCasing(name string) string {
	if strings.Contains(name, "_") {
		return "snake_case"
	}
	first, _ := utf8.DecodeRuneInString(name)
	if unicode.IsUpper(first) {
		return "upper_camel"
	}
	hasUpper := false
	for _, character := range name {
		if unicode.IsUpper(character) {
			hasUpper = true
			break
		}
	}
	if hasUpper {
		return "lower_camel"
	}
	if strings.ToLower(name) == name {
		return "lowercase"
	}
	return "mixed"
}

func providerFromProcess(process ProcessEvidence) System {
	for _, value := range []string{process.Name, filepath.Base(process.ExecutablePath)} {
		if provider := providerFromSignal(value); provider != SystemUnknown {
			return provider
		}
	}
	return SystemUnknown
}

type providerSignal string

func providerFromSignal(value string) System {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.TrimSuffix(normalized, filepath.Ext(normalized))
	switch providerSignal(normalized) {
	case "claude", "claude-code", "anthropic":
		return SystemClaude
	case "cursor":
		return SystemCursor
	case "codex", "openai-codex":
		return SystemCodex
	case "gemini", "gemini-cli", "google-gemini":
		return SystemGemini
	case "vscode", "code":
		return SystemVSCode
	case "copilot", "github-copilot":
		return SystemCopilot
	default:
		return SystemUnknown
	}
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

type nonObjectClassificationInputError struct{}

func (*nonObjectClassificationInputError) Error() string {
	return "classification input must be a JSON object"
}

// MarshalClassification returns stable JSON for durable intake.
func MarshalClassification(classification Classification) json.RawMessage {
	encoded, err := json.Marshal(classification)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}
