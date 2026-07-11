package inferencepb_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"goodkind.io/agent-gate/api/inferencepb"
)

func TestInferenceWireContract(t *testing.T) {
	if inferencepb.Inference_Infer_FullMethodName != "/inference.v1.Inference/Infer" {
		t.Fatalf("method = %q", inferencepb.Inference_Infer_FullMethodName)
	}
	message := (&inferencepb.InferRequest{}).ProtoReflect().Descriptor()
	want := map[protoreflect.Name]protoreflect.FieldNumber{
		"prompt": 1, "input": 2, "output_schema": 3, "context": 4, "model": 5,
		"generation_options": 6,
	}
	for name, number := range want {
		field := message.Fields().ByName(name)
		if field == nil || field.Number() != number {
			t.Fatalf("field %s = %v, want field %d", name, field, number)
		}
	}
	for _, name := range []protoreflect.Name{"prompt", "input", "output_schema", "context", "model"} {
		if message.Fields().ByName(name).Kind() != protoreflect.StringKind {
			t.Fatalf("request field %s is not a string", name)
		}
	}
	if message.Fields().ByName("generation_options").Message().Name() != "GenerationOptions" {
		t.Fatal("generation_options does not reference GenerationOptions")
	}
	if message.Fields().ByName("generation_options").Cardinality() != protoreflect.Optional ||
		!message.Fields().ByName("generation_options").HasPresence() {
		t.Fatal("generation_options cardinality or presence differs from upstream")
	}
	reply := (&inferencepb.InferReply{}).ProtoReflect().Descriptor()
	if reply.Fields().ByName("output_json").Number() != 1 ||
		reply.Fields().ByName("status").Number() != 2 ||
		reply.Fields().ByName("metadata").Number() != 3 {
		t.Fatal("reply field numbers differ from upstream")
	}
	if reply.Fields().ByName("output_json").Kind() != protoreflect.StringKind ||
		reply.Fields().ByName("status").Kind() != protoreflect.EnumKind ||
		reply.Fields().ByName("metadata").Kind() != protoreflect.MessageKind ||
		reply.Fields().ByName("metadata").Message().Name() != "InvocationMetadata" ||
		reply.Fields().ByName("metadata").Cardinality() != protoreflect.Optional ||
		!reply.Fields().ByName("metadata").HasPresence() {
		t.Fatal("reply field contract differs from upstream")
	}
	options := inferencepb.File_inferencepb_inference_proto.Messages().ByName("GenerationOptions")
	wantOptions := map[protoreflect.Name]protoreflect.FieldNumber{
		"reasoning_effort": 1, "max_completion_tokens": 2, "temperature": 3,
	}
	for name, number := range wantOptions {
		field := options.Fields().ByName(name)
		if field == nil || field.Number() != number {
			t.Fatalf("generation option %s = %v, want field %d", name, field, number)
		}
	}
	if options.Fields().ByName("reasoning_effort").Kind() != protoreflect.EnumKind ||
		options.Fields().ByName("max_completion_tokens").Kind() != protoreflect.Int64Kind ||
		options.Fields().ByName("temperature").Kind() != protoreflect.DoubleKind {
		t.Fatal("generation option field types differ from upstream")
	}
	for _, name := range []protoreflect.Name{"reasoning_effort", "max_completion_tokens", "temperature"} {
		if options.Fields().ByName(name).Cardinality() != protoreflect.Optional {
			t.Fatalf("generation option %s cardinality differs from upstream", name)
		}
	}
	for _, name := range []protoreflect.Name{"max_completion_tokens", "temperature"} {
		if !options.Fields().ByName(name).HasPresence() {
			t.Fatalf("generation option %s does not preserve presence", name)
		}
	}
	metadata := inferencepb.File_inferencepb_inference_proto.Messages().ByName("InvocationMetadata")
	wantMetadata := map[protoreflect.Name]protoreflect.FieldNumber{
		"request_id": 1, "service_version": 2, "requested_model": 3,
		"actual_model": 4, "backend_fingerprint": 5, "backend_version": 6,
		"prompt_sha256": 7, "schema_sha256": 8, "prompt_tokens": 9,
		"completion_tokens": 10, "total_tokens": 11, "finish_reason": 12,
		"latency_ms": 13,
	}
	for name, number := range wantMetadata {
		field := metadata.Fields().ByName(name)
		if field == nil || field.Number() != number {
			t.Fatalf("invocation metadata %s = %v, want field %d", name, field, number)
		}
	}
	metadataKinds := map[protoreflect.Name]protoreflect.Kind{
		"request_id": protoreflect.StringKind, "service_version": protoreflect.StringKind,
		"requested_model": protoreflect.StringKind, "actual_model": protoreflect.StringKind,
		"backend_fingerprint": protoreflect.StringKind, "backend_version": protoreflect.StringKind,
		"prompt_sha256": protoreflect.StringKind, "schema_sha256": protoreflect.StringKind,
		"prompt_tokens": protoreflect.Int64Kind, "completion_tokens": protoreflect.Int64Kind,
		"total_tokens": protoreflect.Int64Kind, "finish_reason": protoreflect.StringKind,
		"latency_ms": protoreflect.Int64Kind,
	}
	for name, kind := range metadataKinds {
		field := metadata.Fields().ByName(name)
		if field.Kind() != kind || field.Cardinality() != protoreflect.Optional {
			t.Fatalf("invocation metadata %s kind/cardinality = %s/%s, want %s/optional", name, field.Kind(), field.Cardinality(), kind)
		}
	}
	for _, name := range []protoreflect.Name{"prompt_tokens", "completion_tokens", "total_tokens"} {
		if !metadata.Fields().ByName(name).HasPresence() {
			t.Fatalf("invocation metadata %s does not preserve presence", name)
		}
	}
	for _, name := range []protoreflect.Name{
		"request_id", "service_version", "requested_model", "actual_model",
		"backend_fingerprint", "backend_version", "prompt_sha256", "schema_sha256",
		"finish_reason", "latency_ms",
	} {
		if metadata.Fields().ByName(name).HasPresence() {
			t.Fatalf("invocation metadata %s unexpectedly has presence", name)
		}
	}
	if inferencepb.InferenceStatus_INFERENCE_STATUS_UNSPECIFIED != 0 || inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE != 1 {
		t.Fatal("status enum values differ from upstream")
	}
	if inferencepb.ReasoningEffort_REASONING_EFFORT_UNSPECIFIED != 0 ||
		inferencepb.ReasoningEffort_REASONING_EFFORT_HIGH != 5 ||
		inferencepb.ReasoningEffort_REASONING_EFFORT_XHIGH != 6 {
		t.Fatal("reasoning effort enum values differ from upstream")
	}
}

func TestInvocationTokenUsagePresenceOnWireAndJSON(t *testing.T) {
	usageFields := []protoreflect.Name{"prompt_tokens", "completion_tokens", "total_tokens"}
	tests := []struct {
		name        string
		inputJSON   string
		wantPresent bool
	}{
		{name: "omitted", inputJSON: `{}`, wantPresent: false},
		{
			name:        "null",
			inputJSON:   `{"promptTokens":null,"completionTokens":null,"totalTokens":null}`,
			wantPresent: false,
		},
		{
			name:        "explicit zero",
			inputJSON:   `{"promptTokens":"0","completionTokens":"0","totalTokens":"0"}`,
			wantPresent: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata := &inferencepb.InvocationMetadata{}
			if err := protojson.Unmarshal([]byte(test.inputJSON), metadata); err != nil {
				t.Fatalf("unmarshal JSON: %v", err)
			}
			for _, name := range usageFields {
				present := metadata.ProtoReflect().Has(
					metadata.ProtoReflect().Descriptor().Fields().ByName(name),
				)
				if present != test.wantPresent {
					t.Fatalf("%s JSON presence = %v, want %v", name, present, test.wantPresent)
				}
			}

			wire, err := proto.Marshal(metadata)
			if err != nil {
				t.Fatalf("marshal wire: %v", err)
			}
			decoded := &inferencepb.InvocationMetadata{}
			if err := proto.Unmarshal(wire, decoded); err != nil {
				t.Fatalf("unmarshal wire: %v", err)
			}
			encodedJSON, err := protojson.Marshal(decoded)
			if err != nil {
				t.Fatalf("marshal JSON: %v", err)
			}
			jsonPresent := strings.Contains(string(encodedJSON), `"promptTokens":"0"`) &&
				strings.Contains(string(encodedJSON), `"completionTokens":"0"`) &&
				strings.Contains(string(encodedJSON), `"totalTokens":"0"`)
			if jsonPresent != test.wantPresent {
				t.Fatalf("round-trip JSON presence = %v, want %v: %s", jsonPresent, test.wantPresent, encodedJSON)
			}
		})
	}
}
