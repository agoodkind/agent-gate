// Package regex compiles and matches TOML rule patterns using PCRE2 (libpcre2-8)
// with UTF-8, UCP, optional JIT, and match and depth limits on the hot path.
// Existing rule patterns are written for RE2-shaped syntax and remain valid here.
package regex

import (
	"runtime"
	"unicode/utf8"
	"unsafe"
)

const (
	defaultMatchLimit = 100000
	defaultDepthLimit = 4096
)

// Regexp is a small PCRE2-backed wrapper for the subset of regex APIs used by
// agent-gate's rule engine.
type Regexp struct {
	pattern string
	handle  unsafe.Pointer
}

// Compile compiles a pattern for use in rules.
func Compile(pattern string) (*Regexp, error) {
	handle, err := HandleCompile(pattern, defaultMatchLimit, defaultDepthLimit)
	if err != nil {
		return nil, err
	}

	re := &Regexp{
		pattern: pattern,
		handle:  handle,
	}
	runtime.SetFinalizer(re, (*Regexp).free)

	return re, nil
}

// MustCompile panics if the pattern cannot be compiled.
func MustCompile(pattern string) *Regexp {
	re, err := Compile(pattern)
	if err != nil {
		panic(err)
	}

	return re
}

func (r *Regexp) free() {
	if r == nil {
		return
	}

	HandleFree(r.handle)
	r.handle = nil
}

// MatchString reports whether pattern matches the subject.
//
// MatchString uses the compiled handle and PCRE2 match and depth limits.
func (r *Regexp) MatchString(s string) bool {
	if r == nil || r.handle == nil {
		return false
	}

	rc := HandleMatch(r.handle, s, 0)

	return rc == 1
}

// FindAllStringIndex returns byte offsets for all non-overlapping matches.
// It follows regexp.Regexp.FindAllStringIndex closely enough for diagnostics.
func (r *Regexp) FindAllStringIndex(s string, n int) [][2]int {
	if n == 0 || r == nil || r.handle == nil {
		return nil
	}

	out := make([][2]int, 0)
	offset := 0
	for offset <= len(s) && (n < 0 || len(out) < n) {
		rc := HandleMatch(r.handle, s, offset)
		if rc != 1 {
			break
		}

		start, end, unset, ok := HandleGroupBounds(r.handle, 0)
		if !ok || unset || start < offset || end < start || end > len(s) {
			break
		}

		out = append(out, [2]int{start, end})
		if start == end {
			if end == len(s) {
				break
			}
			_, width := utf8.DecodeRuneInString(s[end:])
			if width == 0 {
				width = 1
			}
			offset = end + width
			continue
		}

		offset = end
	}

	return out
}

// Split behaves like regexp.Regexp.Split for the methods needed by this project.
func (r *Regexp) Split(s string, n int) []string {
	if n == 0 {
		return nil
	}

	if n == 1 || r == nil || r.handle == nil {
		return []string{s}
	}

	parts := make([]string, 0, 4)
	remainder := s
	start := 0

	for n < 0 || len(parts) < n-1 {
		if remainder == "" {
			break
		}

		rc := HandleMatch(r.handle, remainder, 0)
		if rc != 1 {
			break
		}

		positionStart, positionEnd, unset, ok := HandleGroupBounds(r.handle, 0)
		if !ok || unset {
			break
		}

		if positionStart < 0 || positionEnd < positionStart || positionEnd > len(remainder) {
			break
		}

		parts = append(parts, s[start:start+positionStart])
		if positionStart == positionEnd {
			if positionEnd == len(remainder) {
				parts = append(parts, "")

				return parts
			}

			_, width := utf8.DecodeRuneInString(remainder[positionEnd:])
			if width == 0 {
				width = 1
			}

			start += positionEnd + width
			remainder = s[start:]

			continue
		}

		start += positionEnd
		remainder = s[start:]
	}

	parts = append(parts, remainder)

	return parts
}

// FindStringSubmatch returns the full match and capture groups, matching regexp API
// semantics. It returns nil when the pattern does not match.
func (r *Regexp) FindStringSubmatch(s string) []string {
	if r == nil || r.handle == nil {
		return nil
	}

	rc := HandleMatch(r.handle, s, 0)
	if rc != 1 {
		return nil
	}

	capCount := HandleCaptureCount(r.handle)
	out := make([]string, 0, capCount+1)

	for g := uint32(0); g <= capCount; g++ {
		st, en, unset, ok := HandleGroupBounds(r.handle, g)
		if !ok {
			return nil
		}

		if unset {
			out = append(out, "")

			continue
		}

		if st < 0 || en < st || en > len(s) {
			return nil
		}

		out = append(out, s[st:en])
	}

	return out
}
