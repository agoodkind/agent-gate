package shellwrite

import "testing"

// A write hidden inside an interpreter -c body, with or without a cd, must
// surface its resolved target so a branch-aware rule can act on the real file.
// The opaque-shape sentinel is still emitted for default-deny consumers.
func TestExtractWriteTargets_EmbeddedInterpreterBody(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string
	}{
		{"bash -c redirect", `bash -c 'echo x > README.md'`, "/base/README.md"},
		{"bash -c cd then redirect", `bash -c "cd /repo && echo x > f.txt"`, "/repo/f.txt"},
		{"sh -c sed in place", `sh -c 'sed -i s/a/b/ file.go'`, "/base/file.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			targets := ExtractWriteTargets(tc.command, "/base")
			foundPath := false
			foundSentinel := false
			for _, w := range targets {
				if w.Reason == ReasonOK && w.Path == tc.want {
					foundPath = true
				}
				if w.Reason == ReasonUnparsedCommandShape {
					foundSentinel = true
				}
			}
			if !foundPath {
				t.Fatalf("expected resolved embedded write %q, got %+v", tc.want, targets)
			}
			if !foundSentinel {
				t.Fatalf("expected the opaque-shape sentinel to still be present, got %+v", targets)
			}
		})
	}
}
