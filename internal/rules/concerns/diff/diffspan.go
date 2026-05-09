package diff

// NewOnly returns the subset of newIndexes whose captured text in newText is
// not present in oldStrings. The comparison is on string identity rather than
// byte offsets, so a match is considered "additive" only when the same text
// does not also appear among the old matches.
//
// Order is preserved: the returned slice has matches in the same order as in
// newIndexes. Repeated identical match strings in newIndexes that are not in
// oldStrings are all kept; the helper does not deduplicate within new.
func NewOnly(newIndexes [][2]int, oldStrings []string, newText string) [][2]int {
	if len(newIndexes) == 0 {
		return nil
	}
	if len(oldStrings) == 0 {
		out := make([][2]int, len(newIndexes))
		copy(out, newIndexes)
		return out
	}
	oldSet := make(map[string]struct{}, len(oldStrings))
	for _, s := range oldStrings {
		oldSet[s] = struct{}{}
	}
	out := make([][2]int, 0, len(newIndexes))
	for _, idx := range newIndexes {
		text := newText[idx[0]:idx[1]]
		if _, present := oldSet[text]; present {
			continue
		}
		out = append(out, idx)
	}
	return out
}
