package hook

import "testing"

func TestLookupCapabilityKnownPairs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		system HookSystem
		event  string
		want   Capability
	}{
		{"claude pre blocks", SystemClaude, "PreToolUse", CapabilityBlock},
		{"claude post observe", SystemClaude, "PostToolUse", CapabilityObserve},
		{"codex pre blocks", SystemCodex, "PreToolUse", CapabilityBlock},
		{"codex post substitutes", SystemCodex, "PostToolUse", CapabilitySubstitute},
		{"cursor pre blocks", SystemCursor, "preToolUse", CapabilityBlock},
		{"cursor post substitutes", SystemCursor, "postToolUse", CapabilitySubstitute},
		{"cursor after observe", SystemCursor, "afterShellExecution", CapabilityObserve},
		{"gemini before blocks", SystemGemini, "BeforeTool", CapabilityBlock},
		{"gemini after observe", SystemGemini, "AfterTool", CapabilityObserve},
	}
	for _, tc := range cases {
		got := LookupCapability(tc.system, tc.event)
		if got != tc.want {
			t.Errorf("%s: LookupCapability(%v, %q) = %s, want %s", tc.name, tc.system, tc.event, got, tc.want)
		}
	}
}

func TestLookupCapabilityUnknownDefaultsToObserve(t *testing.T) {
	t.Parallel()
	got := LookupCapability(SystemClaude, "NoSuchEvent")
	if got != CapabilityObserve {
		t.Fatalf("unknown event: got %s, want %s", got, CapabilityObserve)
	}
	got = LookupCapability(SystemUnknown, "PreToolUse")
	if got != CapabilityObserve {
		t.Fatalf("unknown system: got %s, want %s", got, CapabilityObserve)
	}
}

func TestCapabilityString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cap  Capability
		want string
	}{
		{CapabilityBlock, "block"},
		{CapabilitySubstitute, "substitute"},
		{CapabilityObserve, "observe"},
	}
	for _, tc := range cases {
		if got := tc.cap.String(); got != tc.want {
			t.Errorf("Capability(%d).String() = %q, want %q", tc.cap, got, tc.want)
		}
	}
}
