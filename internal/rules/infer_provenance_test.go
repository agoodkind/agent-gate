package rules

import (
	"encoding/json"
	"testing"

	"goodkind.io/agent-gate/api/inferencepb"
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
				ReportedPromptHash: "", ReportedSchemaHash: "",
			}

			actual := validCachedInferResult(cached)
			if actual != test.want {
				t.Fatalf("validCachedInferResult(%s) = %t, want %t", test.raw, actual, test.want)
			}
		})
	}
}
