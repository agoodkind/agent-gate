package evaluation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"

	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/rules"
)

const (
	maxLayerMetadataJSONBytes   = 16 * 1024
	maxLayerMetadataStringBytes = 1024
)

type reportedHashStatus string

const (
	reportedHashStatusAbsent      reportedHashStatus = "absent"
	reportedHashStatusMatch       reportedHashStatus = "match"
	reportedHashStatusMismatch    reportedHashStatus = "mismatch"
	reportedHashStatusUnavailable reportedHashStatus = "unavailable"
)

type layerMetadataVersion struct {
	SchemaVersion int `json:"schema_version"`
}

// LayerMetadataV2Wire is the strict wire envelope before typed normalization.
type LayerMetadataV2Wire struct {
	SchemaVersion      int             `json:"schema_version"`
	RuleName           string          `json:"rule_name,omitempty"`
	ConditionIndex     int             `json:"condition_index,omitempty"`
	SkipReason         string          `json:"skip_reason,omitempty"`
	VerifiedProvenance json.RawMessage `json:"verified_provenance"`
	UpstreamMetadata   json.RawMessage `json:"upstream_metadata"`
	GenerationOptions  json.RawMessage `json:"generation_options,omitempty"`
}

type normalizedLayerMetadataV2 struct {
	SchemaVersion      int                      `json:"schema_version"`
	RuleName           string                   `json:"rule_name,omitempty"`
	ConditionIndex     int                      `json:"condition_index,omitempty"`
	SkipReason         string                   `json:"skip_reason,omitempty"`
	VerifiedProvenance rules.VerifiedProvenance `json:"verified_provenance"`
	UpstreamMetadata   rules.UpstreamMetadata   `json:"upstream_metadata"`
	GenerationOptions  json.RawMessage          `json:"generation_options,omitempty"`
}

// UnmarshalLayerMetadata validates metadata and returns its safe export encoding.
func UnmarshalLayerMetadata(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > maxLayerMetadataJSONBytes || !json.Valid(raw) {
		return nil, errors.New("layer metadata is invalid or exceeds byte limit")
	}
	var version layerMetadataVersion
	if err := json.Unmarshal(raw, &version); err != nil {
		return nil, fmt.Errorf("decode layer metadata version: %s", err.Error())
	}
	if version.SchemaVersion != 2 {
		return append(json.RawMessage(nil), raw...), nil
	}
	metadata, err := UnmarshalLayerMetadataV2(raw)
	if err != nil {
		return nil, err
	}
	if metadata.SchemaVersion != 2 || metadata.ConditionIndex < 0 {
		return nil, errors.New("v2 layer metadata identity is invalid")
	}
	if err := validateMetadataString("rule_name", metadata.RuleName); err != nil {
		return nil, err
	}
	if err := validateMetadataString("skip_reason", metadata.SkipReason); err != nil {
		return nil, err
	}
	verified, err := UnmarshalVerifiedProvenance(metadata.VerifiedProvenance)
	if err != nil {
		return nil, err
	}
	if err := validateVerifiedProvenance(verified); err != nil {
		return nil, err
	}
	upstream, err := UnmarshalUpstreamMetadataEnvelope(metadata.UpstreamMetadata)
	if err != nil {
		return nil, err
	}
	normalizedUpstream, err := rules.UnmarshalUpstreamMetadata(upstream)
	if err != nil {
		return nil, fmt.Errorf("validate upstream metadata: %s", err.Error())
	}
	generationOptions, err := UnmarshalGenerationOptions(metadata.GenerationOptions)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(normalizedLayerMetadataV2{
		SchemaVersion: metadata.SchemaVersion, RuleName: metadata.RuleName,
		ConditionIndex: metadata.ConditionIndex, SkipReason: metadata.SkipReason,
		VerifiedProvenance: verified, UpstreamMetadata: normalizedUpstream,
		GenerationOptions: generationOptions,
	})
	if err != nil {
		return nil, fmt.Errorf("encode normalized v2 layer metadata: %s", err.Error())
	}
	if len(encoded) > maxLayerMetadataJSONBytes {
		return nil, errors.New("normalized v2 layer metadata exceeds byte limit")
	}
	return encoded, nil
}

// UnmarshalLayerMetadataV2 strictly decodes the v2 metadata envelope.
func UnmarshalLayerMetadataV2(raw json.RawMessage) (LayerMetadataV2Wire, error) {
	var metadata LayerMetadataV2Wire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return LayerMetadataV2Wire{}, fmt.Errorf(
			"decode strict v2 layer metadata: %s",
			err.Error(),
		)
	}
	if len(metadata.VerifiedProvenance) == 0 || len(metadata.UpstreamMetadata) == 0 {
		return LayerMetadataV2Wire{}, errors.New("v2 layer metadata provenance is required")
	}
	return metadata, nil
}

// UnmarshalVerifiedProvenance strictly decodes locally verified provenance.
func UnmarshalVerifiedProvenance(
	raw json.RawMessage,
) (rules.VerifiedProvenance, error) {
	var verified rules.VerifiedProvenance
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&verified); err != nil {
		return rules.VerifiedProvenance{}, fmt.Errorf(
			"decode verified provenance: %s",
			err.Error(),
		)
	}
	return verified, nil
}

// UnmarshalUpstreamMetadataEnvelope strictly decodes the untrusted envelope.
func UnmarshalUpstreamMetadataEnvelope(raw json.RawMessage) (rules.UpstreamMetadata, error) {
	var upstream rules.UpstreamMetadata
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&upstream); err != nil {
		return rules.UpstreamMetadata{}, fmt.Errorf(
			"decode upstream metadata envelope: %s",
			err.Error(),
		)
	}
	return upstream, nil
}

// UnmarshalGenerationOptions returns canonical strict protobuf JSON.
func UnmarshalGenerationOptions(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	options := new(inferencepb.GenerationOptions)
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, options); err != nil {
		return nil, fmt.Errorf("decode generation options: %s", err.Error())
	}
	encoded, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(options)
	if err != nil {
		return nil, fmt.Errorf("encode generation options: %s", err.Error())
	}
	return append(json.RawMessage(nil), encoded...), nil
}

func validateVerifiedProvenance(value rules.VerifiedProvenance) error {
	fields := map[string]string{
		"requested_model": value.RequestedModel,
		"endpoint_hash":   value.EndpointHash,
		"cache_key_hash":  value.CacheKeyHash,
		"input_hash":      value.InputHash,
		"prompt_sha256":   value.PromptSHA256,
		"schema_sha256":   value.SchemaSHA256,
	}
	for name, field := range fields {
		if err := validateMetadataString(name, field); err != nil {
			return err
		}
	}
	if !validReportedHashStatus(value.ReportedPromptHashStatus) ||
		!validReportedHashStatus(value.ReportedSchemaHashStatus) {
		return errors.New("verified provenance reported hash status is invalid")
	}
	return nil
}

func validReportedHashStatus(value string) bool {
	switch reportedHashStatus(value) {
	case reportedHashStatusAbsent, reportedHashStatusMatch, reportedHashStatusMismatch,
		reportedHashStatusUnavailable:
		return true
	default:
		return false
	}
}

func validateMetadataString(name string, value string) error {
	if len(value) > maxLayerMetadataStringBytes {
		return fmt.Errorf("layer metadata field %q exceeds byte limit", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("layer metadata field %q is not UTF-8", name)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("layer metadata field %q contains control characters", name)
		}
	}
	return nil
}
