package rules

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"

	"goodkind.io/agent-gate/api/inferencepb"
)

const (
	// MaxUpstreamMetadataJSONBytes bounds the untrusted invocation metadata snapshot.
	MaxUpstreamMetadataJSONBytes   = 4096
	maxUpstreamMetadataStringBytes = 512
)

// UnmarshalUpstreamMetadata validates provenance, status consistency, and the
// strict InvocationMetadata schema before returning canonical protobuf JSON.
func UnmarshalUpstreamMetadata(metadata UpstreamMetadata) (UpstreamMetadata, error) {
	if metadata.Source != "inference_reply" || metadata.Trust != "untrusted" {
		return UpstreamMetadata{}, errors.New("upstream metadata provenance is invalid")
	}
	switch metadata.Status {
	case UpstreamMetadataPresent:
		return UnmarshalPresentUpstreamMetadata(metadata)
	case UpstreamMetadataAbsent, UpstreamMetadataOmittedMalformed,
		UpstreamMetadataOmittedOversize:
		if len(metadata.Raw) != 0 {
			return UpstreamMetadata{}, errors.New("non-present upstream metadata contains raw claims")
		}
		return UpstreamMetadata{
			Source: metadata.Source, Trust: metadata.Trust, Status: metadata.Status, Raw: nil,
		}, nil
	default:
		return UpstreamMetadata{}, fmt.Errorf("upstream metadata status %q is invalid", metadata.Status)
	}
}

// UnmarshalPresentUpstreamMetadata strictly decodes present invocation claims.
func UnmarshalPresentUpstreamMetadata(metadata UpstreamMetadata) (UpstreamMetadata, error) {
	if len(metadata.Raw) == 0 {
		return UpstreamMetadata{}, errors.New("present upstream metadata has no raw claims")
	}
	if len(metadata.Raw) > MaxUpstreamMetadataJSONBytes {
		return UpstreamMetadata{}, errors.New("upstream metadata exceeds byte limit")
	}
	parsed := new(inferencepb.InvocationMetadata)
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(
		metadata.Raw,
		parsed,
	); err != nil {
		return UpstreamMetadata{}, fmt.Errorf("decode invocation metadata: %s", err.Error())
	}
	if err := validateInvocationMetadataStrings(parsed); err != nil {
		return UpstreamMetadata{}, err
	}
	encoded, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(parsed)
	if err != nil || !json.Valid(encoded) {
		return UpstreamMetadata{}, errors.New("encode invocation metadata")
	}
	if len(encoded) > MaxUpstreamMetadataJSONBytes {
		return UpstreamMetadata{}, errors.New("canonical upstream metadata exceeds byte limit")
	}
	return UpstreamMetadata{
		Source: metadata.Source,
		Trust:  metadata.Trust,
		Status: metadata.Status,
		Raw:    append(json.RawMessage(nil), encoded...),
	}, nil
}

func validateInvocationMetadataStrings(metadata *inferencepb.InvocationMetadata) error {
	reflection := metadata.ProtoReflect()
	fields := reflection.Descriptor().Fields()
	fieldCount := fields.Len()
	for i := range fieldCount {
		field := fields.Get(i)
		if field.Kind() != protoreflect.StringKind || !reflection.Has(field) {
			continue
		}
		value := reflection.Get(field).String()
		if len(value) > maxUpstreamMetadataStringBytes {
			return fmt.Errorf("invocation metadata field %q exceeds byte limit", field.Name())
		}
		if !utf8.ValidString(value) {
			return fmt.Errorf("invocation metadata field %q is not UTF-8", field.Name())
		}
		for _, character := range value {
			if unicode.IsControl(character) {
				return fmt.Errorf("invocation metadata field %q contains control characters", field.Name())
			}
		}
	}
	return nil
}
