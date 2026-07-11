package hook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
	gkversion "goodkind.io/gklog/version"
)

type stagedRuleEvaluation struct {
	violations []rules.Violation
	trace      rules.DecisionTrace
}

func evaluateStagedRules(
	ctx context.Context,
	cfg *config.Config,
	system string,
	eventName string,
	fields rules.FieldSet,
	ruleSet []config.Rule,
	getenv func(string) string,
	normalizedJSON json.RawMessage,
) stagedRuleEvaluation {
	if len(ruleSet) == 0 {
		violations := rules.EvaluateAll(ctx, system, eventName, fields, ruleSet, getenv)
		detailed := rules.EvaluateAllDetailed(
			ctx, system, eventName, fields, ruleSet, getenv, normalizedJSON, gkversion.Version,
		)
		return stagedRuleEvaluation{violations: violations, trace: detailed.Trace}
	}
	deterministicRules, inferenceRules := partitionInferenceRules(ruleSet)
	deterministic := rules.EvaluateAllDetailed(
		ctx,
		system,
		eventName,
		fields,
		deterministicRules,
		getenv,
		normalizedJSON,
		gkversion.Version,
	)
	violations := deterministic.Violations
	detailedResults := []rules.DetailedEvaluation{deterministic}
	if len(blockingMatches(violations)) > 0 || len(inferenceRules) == 0 {
		trace := mergeStagedDecisionTrace(
			deterministic.Trace,
			ruleSet,
			detailedResults,
			violations,
			system,
			eventName,
		)
		if len(inferenceRules) > 0 {
			trace.Layers = append(
				trace.Layers,
				skippedStagedInferenceLayers(inferenceRules, len(trace.Layers))...,
			)
		}
		return stagedRuleEvaluation{violations: violations, trace: trace}
	}

	inferenceCtx, cancel := context.WithTimeout(ctx, cfg.HookInferencePhaseTimeout())
	defer cancel()
	for i := range inferenceRules {
		if inferenceCtx.Err() != nil {
			break
		}
		inference := rules.EvaluateAllDetailed(
			inferenceCtx,
			system,
			eventName,
			fields,
			[]config.Rule{inferenceRules[i]},
			getenv,
			normalizedJSON,
			gkversion.Version,
		)
		detailedResults = append(detailedResults, inference)
		violations = append(violations, inference.Violations...)
	}
	return stagedRuleEvaluation{
		violations: violations,
		trace: mergeStagedDecisionTrace(
			deterministic.Trace,
			ruleSet,
			detailedResults,
			violations,
			system,
			eventName,
		),
	}
}

func compactTraceJSON(value []byte) json.RawMessage {
	var compacted bytes.Buffer
	if json.Compact(&compacted, value) != nil {
		return json.RawMessage(`{}`)
	}
	return compacted.Bytes()
}

func mergeStagedDecisionTrace(
	deterministic rules.DecisionTrace,
	ruleSet []config.Rule,
	results []rules.DetailedEvaluation,
	violations []rules.Violation,
	system string,
	eventName string,
) rules.DecisionTrace {
	decisionsByName := make(map[string]rules.RuleDecision)
	for resultIndex, result := range results {
		for _, decision := range result.Trace.Deterministic.CheckedRules {
			decisionsByName[decision.RuleName] = decision
		}
		if resultIndex == 0 {
			continue
		}
		layerOffset := len(deterministic.Layers)
		for _, layer := range result.Trace.Layers {
			if layer.ParentTraceIndex != nil && *layer.ParentTraceIndex > 0 {
				parent := *layer.ParentTraceIndex + layerOffset
				layer.ParentTraceIndex = &parent
			}
			deterministic.Layers = append(deterministic.Layers, layer)
		}
	}
	decisions := make([]rules.RuleDecision, 0, len(ruleSet))
	for i := range ruleSet {
		if decision, ok := decisionsByName[ruleSet[i].Name]; ok {
			decisions = append(decisions, decision)
			continue
		}
		decisions = append(decisions, rules.RuleDecision{
			RuleName: ruleSet[i].Name, Status: "skipped",
			SkipReason: "prior_condition_nonmatch", Matched: false,
		})
	}
	deterministic.Deterministic.CheckedRules = decisions
	deterministic.Deterministic.OutputJSON = stagedDeterministicOutput(
		decisions,
		violations,
		LookupCapability(SystemFromString(system), eventName).String(),
	)
	deterministic.Deterministic.OutputHash = hookTraceHash(deterministic.Deterministic.OutputJSON)
	return deterministic
}

type stagedRuleOutput struct {
	Rules                      []stagedRuleOutputDecision `json:"rules"`
	ProviderBlockingCapability string                     `json:"provider_blocking_capability"`
	Result                     string                     `json:"result"`
}

type stagedRuleOutputDecision struct {
	rules.RuleDecision
	ViolationIdentities []stagedViolationIdentity `json:"violation_identities"`
}

type stagedViolationIdentity struct {
	RuleName  string `json:"rule_name"`
	FieldPath string `json:"field_path,omitempty"`
	FilePath  string `json:"file_path,omitempty"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
}

func stagedDeterministicOutput(
	decisions []rules.RuleDecision,
	violations []rules.Violation,
	capability string,
) json.RawMessage {
	outputRules := make([]stagedRuleOutputDecision, len(decisions))
	for i := range decisions {
		outputRules[i].RuleDecision = decisions[i]
		outputRules[i].ViolationIdentities = make([]stagedViolationIdentity, 0)
		for _, violation := range violations {
			if violation.RuleName != decisions[i].RuleName {
				continue
			}
			outputRules[i].ViolationIdentities = append(
				outputRules[i].ViolationIdentities,
				stagedViolationIdentity{
					RuleName: violation.RuleName, FieldPath: violation.FieldPath,
					FilePath: violation.FilePath, Start: violation.Start, End: violation.End,
				},
			)
		}
	}
	result := "allow"
	if len(violations) > 0 {
		result = "block"
	}
	encoded, err := json.Marshal(stagedRuleOutput{
		Rules: outputRules, ProviderBlockingCapability: capability, Result: result,
	})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func skippedStagedInferenceLayers(ruleSet []config.Rule, layerOffset int) []rules.LayerTrace {
	layers := make([]rules.LayerTrace, 0)
	for ruleIndex := range ruleSet {
		rule := &ruleSet[ruleIndex]
		for conditionIndex := range rule.Conditions {
			condition := &rule.Conditions[conditionIndex]
			if config.ConditionKind(condition.Kind) != config.ConditionKindInfer {
				continue
			}
			parent := 0
			if condition.ContextSource != "" {
				layers = append(layers, skippedStagedLayer(
					rule.Name,
					conditionIndex,
					rule.Name+"/"+condition.LayerName+"/context",
					"context",
					condition.Model,
					parent,
				))
				parent = layerOffset + len(layers)
			}
			layers = append(layers, skippedStagedLayer(
				rule.Name, conditionIndex, condition.LayerName, "inference", condition.Model, parent,
			))
		}
	}
	return layers
}

func skippedStagedLayer(
	ruleName string,
	conditionIndex int,
	layerName string,
	kind string,
	model string,
	parent int,
) rules.LayerTrace {
	emptyJSON := json.RawMessage(`{}`)
	return rules.LayerTrace{
		RuleName: ruleName, ConditionIndex: conditionIndex, LayerName: layerName,
		Kind: kind, Status: "skipped", Outcome: "", SkipReason: "prior_condition_nonmatch",
		ParentTraceIndex: &parent, StartedAt: time.Time{}, CompletedAt: time.Time{},
		InputReference: "intake.normalized_json", InputJSON: emptyJSON, OutputJSON: emptyJSON,
		InputHash: hookTraceHash(emptyJSON), OutputHash: hookTraceHash(emptyJSON),
		ServiceName: "", ServiceVersion: "",
		VerifiedProvenance: rules.VerifiedProvenance{
			RequestedModel: model, EndpointHash: "", CacheKeyHash: "", InputHash: "",
			PromptSHA256: "", SchemaSHA256: "", ReportedPromptHashStatus: "absent",
			ReportedSchemaHashStatus: "absent",
		},
		CacheStatus:  "",
		CacheKeyHash: "", CacheEntryVersion: nil, CacheExpiresAt: nil,
		UpstreamMetadata: rules.UpstreamMetadata{
			Source: "inference_reply", Trust: "untrusted",
			Status: rules.UpstreamMetadataAbsent, Raw: nil,
		},
		ErrorCode: "", ErrorMessage: "", RetryCount: 0,
	}
}

func hookTraceHash(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}
