package rules

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/hotkv"
)

func TestUnmarshalUpstreamMetadataPreservesNormalizationClaimsCanonically(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{
			name: "normalized output",
			raw: json.RawMessage(`{
				"outputNormalized":true,
				"normalizationKind":"bare_enum_object",
				"rawOutputSha256":"sha256:raw",
				"upstreamResponseId":"response-1"
			}`),
			want: `{"output_normalized":true,"normalization_kind":"bare_enum_object","raw_output_sha256":"sha256:raw","upstream_response_id":"response-1"}`,
		},
		{
			name: "explicit false follows proto3 canonical form",
			raw: json.RawMessage(`{
				"outputNormalized":false,
				"normalizationKind":"none",
				"rawOutputSha256":"sha256:raw",
				"upstreamResponseId":"response-2"
			}`),
			want: `{"normalization_kind":"none","raw_output_sha256":"sha256:raw","upstream_response_id":"response-2"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata, err := UnmarshalUpstreamMetadata(UpstreamMetadata{
				Source: "inference_reply", Trust: "untrusted",
				Status: UpstreamMetadataPresent, Raw: test.raw,
			})
			if err != nil {
				t.Fatalf("UnmarshalUpstreamMetadata: %v", err)
			}
			var compact bytes.Buffer
			if err := json.Compact(&compact, metadata.Raw); err != nil {
				t.Fatalf("compact canonical metadata: %v", err)
			}
			if compact.String() != test.want {
				t.Fatalf("canonical raw = %s, want %s", compact.String(), test.want)
			}
		})
	}
}

func TestUnmarshalUpstreamMetadataBoundsNewIdentifierClaims(t *testing.T) {
	for _, field := range []string{"raw_output_sha256", "upstream_response_id"} {
		t.Run(field, func(t *testing.T) {
			raw, err := json.Marshal(map[string]string{field: strings.Repeat("x", 513)})
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			_, err = UnmarshalUpstreamMetadata(UpstreamMetadata{
				Source: "inference_reply", Trust: "untrusted",
				Status: UpstreamMetadataPresent, Raw: raw,
			})
			if err == nil || !strings.Contains(err.Error(), field) ||
				!strings.Contains(err.Error(), "exceeds byte limit") {
				t.Fatalf("error = %v, want bounded %s error", err, field)
			}
		})
	}
}

func TestBoundedUpstreamMetadataOmitsMalformedProtoJSON(t *testing.T) {
	metadata := &inferencepb.InvocationMetadata{RequestId: string([]byte{0xff})}

	bounded := boundedUpstreamMetadata(metadata)

	if bounded.Status != "omitted_malformed" || len(bounded.Raw) != 0 ||
		bounded.Source != "inference_reply" || bounded.Trust != "untrusted" {
		t.Fatalf("bounded metadata = %+v", bounded)
	}
}

func TestValidCachedInferResultStrictlyParsesMetadataProtoJSON(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want bool
	}{
		{name: "scalar", raw: json.RawMessage(`42`), want: false},
		{name: "array", raw: json.RawMessage(`[]`), want: false},
		{name: "unknown field", raw: json.RawMessage(`{"unknown":"claim"}`), want: false},
		{name: "malformed", raw: json.RawMessage(`{"request_id":`), want: false},
		{
			name: "valid optional zero",
			raw:  json.RawMessage(`{"request_id":"request-1","prompt_tokens":"0"}`),
			want: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cached := cachedInferResult{
				SchemaVersion: 2, Matched: true,
				OutputJSON: json.RawMessage(`{"decision":"block"}`),
				UpstreamMetadata: UpstreamMetadata{
					Source: "inference_reply", Trust: "untrusted",
					Status: UpstreamMetadataPresent, Raw: test.raw,
				},
			}

			_, actual := cachedInvocationMetadata(cached)
			if actual != test.want {
				t.Fatalf("cachedInvocationMetadata(%s) = %t, want %t", test.raw, actual, test.want)
			}
		})
	}
}

func TestCacheLookupDerivesHashesFromRawMetadata(t *testing.T) {
	encoded := []byte(`{
		"schema_version":2,
		"matched":true,
		"output_json":{"decision":"block"},
		"upstream_metadata":{
			"source":"inference_reply",
			"trust":"untrusted",
			"status":"present",
			"raw":{"prompt_sha256":"wrong-prompt","schema_sha256":"wrong-schema"}
		},
		"reported_prompt_hash":"sha256:local-prompt",
		"reported_schema_hash":"sha256:local-schema"
	}`)
	runtime := inferRuntimeWithPoisonedCache(t, "contradictory", encoded)

	result, ok := runtime.cacheLookup("contradictory")

	if !ok || result.reportedPromptHash != "wrong-prompt" ||
		result.reportedSchemaHash != "wrong-schema" || result.reportedHashesUnavailable {
		t.Fatalf("cached result = %+v, ok = %t", result, ok)
	}
}

func TestCacheLookupReportsUnavailableOnlyForOmittedMetadata(t *testing.T) {
	tests := []struct {
		name       string
		status     UpstreamMetadataStatus
		wantStatus string
	}{
		{name: "absent", status: UpstreamMetadataAbsent, wantStatus: "absent"},
		{
			name: "malformed", status: UpstreamMetadataOmittedMalformed,
			wantStatus: "unavailable",
		},
		{
			name: "oversize", status: UpstreamMetadataOmittedOversize,
			wantStatus: "unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(cachedInferResult{
				SchemaVersion: 2, Matched: true,
				OutputJSON: json.RawMessage(`{"decision":"block"}`),
				UpstreamMetadata: UpstreamMetadata{
					Source: "inference_reply", Trust: "untrusted", Status: test.status, Raw: nil,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			runtime := inferRuntimeWithPoisonedCache(t, test.name, encoded)
			result, ok := runtime.cacheLookup(test.name)
			if !ok {
				t.Fatal("cache lookup missed valid entry")
			}
			actual := reportedHashStatus(
				result.reportedPromptHash, "sha256:local", result.reportedHashesUnavailable,
			)
			if actual != test.wantStatus {
				t.Fatalf("reported status = %q, want %q", actual, test.wantStatus)
			}
		})
	}
}

func inferRuntimeWithPoisonedCache(t *testing.T, key string, encoded []byte) *InferRuntime {
	t.Helper()
	cache := hotkv.New(hotkv.Options{
		MaxEntries: 0, MaxValueBytes: 0, PruneInterval: 0,
	})
	t.Cleanup(cache.Close)
	_, _, err := cache.Set(inferenceCacheNamespace, key, encoded, hotkv.SetOptions{
		Mode: hotkv.SetModeAny, TTL: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewInferRuntimeWithCache(nil, cache)
	t.Cleanup(runtime.Close)
	return runtime
}
