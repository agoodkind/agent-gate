package rules

import (
	"encoding/json"
	"testing"

	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/hotkv"
)

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
