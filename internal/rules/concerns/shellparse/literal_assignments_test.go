package shellparse

import "testing"

func TestExpandLiteralAssignments(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{name: "literal value", command: `R=/repo/main; echo "$R"`, want: `R=/repo/main; echo "/repo/main"`},
		{name: "suffix", command: `R=/repo/main; cat "${R}/main.go"`, want: `R=/repo/main; cat "/repo/main/main.go"`},
		{name: "distinct assignments", command: `A=/a B=/b; cp "$A/x" "$B/y"`, want: `A=/a B=/b; cp "/a/x" "/b/y"`},
		{name: "literal reassignment", command: `R=/one; R=/two; cat "$R/x"`, want: `R=/one; R=/two; cat "/two/x"`},
		{name: "use before reassignment", command: `R=/one; cat "$R/a"; R=/two; cat "$R/b"`, want: `R=/one; cat "/one/a"; R=/two; cat "/two/b"`},
		{name: "dynamic reassignment", command: `R=/one; R=$(pwd); cat "$R/x"`, want: `R=/one; R=$(pwd); cat "$R/x"`},
		{name: "use before dynamic reassignment", command: `R=/one; cat "$R/a"; R=$(pwd); cat "$R/b"`, want: `R=/one; cat "/one/a"; R=$(pwd); cat "$R/b"`},
		{name: "conditional reassignment", command: `R=/one; false && R=/two; cat "$R/x"`, want: `R=/one; false && R=/two; cat "$R/x"`},
		{name: "command substitution", command: `R=$(pwd); cat "$R/x"`, want: `R=$(pwd); cat "$R/x"`},
		{name: "later assignment", command: `echo > "$R/file"; R=/repo`, want: `echo > "$R/file"; R=/repo`},
		{name: "escaped reference", command: `R=/repo; echo "\$R/file"`, want: `R=/repo; echo "\$R/file"`},
		{name: "single quoted reference", command: `R=/repo; cat '$R/x'`, want: `R=/repo; cat '$R/x'`},
		{name: "malformed single quote", command: `R=/repo; cat '$R/x`, want: `R=/repo; cat '$R/x`},
		{name: "malformed double quote", command: `R=/repo; cat "$R/x`, want: `R=/repo; cat "$R/x`},
		{name: "parameter default", command: `R=/repo; cat "${R:-/tmp}/x"`, want: `R=/repo; cat "${R:-/tmp}/x"`},
		{name: "escaped unquoted reference", command: `R=/repo; printf '%s' \$R`, want: `R=/repo; printf '%s' \$R`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ExpandLiteralAssignments(test.command); got != test.want {
				t.Fatalf("ExpandLiteralAssignments(%q) = %q, want %q", test.command, got, test.want)
			}
		})
	}
}
