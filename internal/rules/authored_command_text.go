package rules

import "strings"

// AuthoredCommandText returns the command text authored by an agent.
//
// The method keeps shell text outside patch envelopes. It removes context and
// deleted lines inside each envelope.
//
// Example:
//
//	fields := FieldSet{ToolInputCommand: "*** Begin Patch\n+return nil"}
//	fmt.Println(fields.AuthoredCommandText())
//	// Output: return nil
func (fields FieldSet) AuthoredCommandText() string {
	body := fields.ToolInputCommand
	if !strings.Contains(body, "*** Begin Patch") {
		return body
	}

	var kept []string
	inPatch := false

	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)

		if !inPatch {
			if trimmed == "*** Begin Patch" {
				inPatch = true

				continue
			}

			kept = append(kept, line)

			continue
		}

		if trimmed == "*** End Patch" {
			inPatch = false

			continue
		}

		if strings.HasPrefix(line, "+") {
			kept = append(kept, line[1:])
		}
	}

	return strings.Join(kept, "\n")
}
