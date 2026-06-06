package hook

// snippetMaxLen bounds the length of audit-log string snippets so a single
// oversized field cannot dominate a log line.
const snippetMaxLen = 200

// truncate shortens s to snippetMaxLen runes, replacing the tail with an
// ellipsis when it overflows.
func truncate(s string) string {
	const ellipsis = "..."
	if len(s) <= snippetMaxLen {
		return s
	}
	return s[:snippetMaxLen-len(ellipsis)] + ellipsis
}
