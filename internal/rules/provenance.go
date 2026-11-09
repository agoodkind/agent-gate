package rules

import (
	"encoding/json"
	"strings"

	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/config"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	hashStatusAbsent = "absent"
	hashStatusMatch  = "match"
)

func invocationHashMismatch(
	metadata *inferencepb.InvocationMetadata,
	localPromptHash string,
	localSchemaHash string,
) bool {
	if metadata == nil {
		return false
	}

	return hashValueMismatch(metadata.GetPromptSha256(), localPromptHash) ||
		hashValueMismatch(metadata.GetSchemaSha256(), localSchemaHash)
}

func hashValueMismatch(upstream string, local string) bool {
	if upstream == "" {
		return false
	}

	return strings.TrimPrefix(upstream, "sha256:") != strings.TrimPrefix(local, "sha256:")
}

func applyInvocationMetadata(
	result inferResult,
	metadata *inferencepb.InvocationMetadata,
) inferResult {
	result.upstreamMetadata = boundedUpstreamMetadata(metadata)
	if metadata != nil {
		result.reportedPromptHash = metadata.GetPromptSha256()
		result.reportedSchemaHash = metadata.GetSchemaSha256()
	}

	return result
}

func boundedUpstreamMetadata(metadata *inferencepb.InvocationMetadata) UpstreamMetadata {
	bounded := emptyUpstreamMetadata()
	if metadata == nil {
		return bounded
	}

	encoded, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(metadata)
	if err != nil || !json.Valid(encoded) {
		bounded.Status = UpstreamMetadataOmittedMalformed

		return bounded
	}

	if len(encoded) > MaxUpstreamMetadataJSONBytes {
		bounded.Status = UpstreamMetadataOmittedOversize

		return bounded
	}

	bounded.Status = UpstreamMetadataPresent
	bounded.Raw = append(json.RawMessage(nil), encoded...)

	return bounded
}

func verifiedInferenceProvenance(
	condition *config.Condition,
	input string,
	cacheKey string,
	result inferResult,
) VerifiedProvenance {
	promptHash := traceJSONHash([]byte(condition.Prompt))
	schemaHash := traceJSONHash([]byte(condition.OutputSchema))

	return VerifiedProvenance{
		RequestedModel: condition.Model,
		EndpointHash:   traceJSONHash([]byte(condition.Endpoint)),
		CacheKeyHash:   traceJSONHash([]byte(cacheKey)),
		InputHash:      traceJSONHash([]byte(input)),
		PromptSHA256:   promptHash,
		SchemaSHA256:   schemaHash,
		ReportedPromptHashStatus: reportedHashStatus(
			result.reportedPromptHash,
			promptHash,
			result.reportedHashesUnavailable,
		),
		ReportedSchemaHashStatus: reportedHashStatus(
			result.reportedSchemaHash,
			schemaHash,
			result.reportedHashesUnavailable,
		),
	}
}

func reportedHashStatus(reported string, local string, unavailable bool) string {
	if reported == "" {
		if unavailable {
			return "unavailable"
		}

		return hashStatusAbsent
	}

	if hashValueMismatch(reported, local) {
		return "mismatch"
	}

	return hashStatusMatch
}
