package shellparse

import "testing"

func TestExpandEnvironmentVariables(t *testing.T) {
	values := map[string]string{
		"TARGET": "/repo/main/file.txt",
		"SPACED": "/repo/main/a b.txt",
	}
	getenv := func(name string) string {
		return values[name]
	}
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{name: "double quoted", command: `echo > "$TARGET"`, want: `echo > "/repo/main/file.txt"`},
		{name: "braced", command: `cat "${TARGET}"`, want: `cat "/repo/main/file.txt"`},
		{name: "unquoted spaced", command: `cat $SPACED`, want: `cat $SPACED`},
		{name: "single quoted", command: `cat '$TARGET'`, want: `cat '$TARGET'`},
		{name: "escaped", command: `cat \$TARGET`, want: `cat \$TARGET`},
		{name: "unknown", command: `cat "$UNKNOWN"`, want: `cat "$UNKNOWN"`},
		{name: "command substitution", command: `cat "$(printf x)"`, want: `cat "$(printf x)"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ExpandEnvironmentVariables(test.command, getenv); got != test.want {
				t.Fatalf("ExpandEnvironmentVariables(%q) = %q, want %q", test.command, got, test.want)
			}
		})
	}
}

func TestReferencedEnvironmentVariables(t *testing.T) {
	command := `echo "$TARGET" "$TARGET" '${PRIVATE}' \$ESCAPED "$OTHER" $UNQUOTED`
	got := ReferencedEnvironmentVariables(command)
	want := []string{"OTHER", "TARGET"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ReferencedEnvironmentVariables() = %v, want %v", got, want)
	}
}
