package inferencepb_test

import (
	"testing"

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
	reply := (&inferencepb.InferReply{}).ProtoReflect().Descriptor()
	if reply.Fields().ByName("output_json").Number() != 1 ||
		reply.Fields().ByName("status").Number() != 2 ||
		reply.Fields().ByName("metadata").Number() != 3 {
		t.Fatal("reply field numbers differ from upstream")
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
	if inferencepb.InferenceStatus_INFERENCE_STATUS_UNSPECIFIED != 0 || inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE != 1 {
		t.Fatal("status enum values differ from upstream")
	}
	if inferencepb.ReasoningEffort_REASONING_EFFORT_UNSPECIFIED != 0 ||
		inferencepb.ReasoningEffort_REASONING_EFFORT_HIGH != 5 ||
		inferencepb.ReasoningEffort_REASONING_EFFORT_XHIGH != 6 {
		t.Fatal("reasoning effort enum values differ from upstream")
	}
}
