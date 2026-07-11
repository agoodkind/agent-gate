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
	}
	for name, number := range want {
		field := message.Fields().ByName(name)
		if field == nil || field.Number() != number || field.Kind() != protoreflect.StringKind {
			t.Fatalf("field %s = %v, want string %d", name, field, number)
		}
	}
	reply := (&inferencepb.InferReply{}).ProtoReflect().Descriptor()
	if reply.Fields().ByName("output_json").Number() != 1 || reply.Fields().ByName("status").Number() != 2 {
		t.Fatal("reply field numbers differ from upstream")
	}
	if inferencepb.InferenceStatus_INFERENCE_STATUS_UNSPECIFIED != 0 || inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE != 1 {
		t.Fatal("status enum values differ from upstream")
	}
}
