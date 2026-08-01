package updateopts

import "strings"

// isLocalBuild reports whether the running binary was built locally rather than
// downloaded from a release, so the updater must leave it alone.
//
// The updater already refuses to replace a build made from a dirty worktree,
// but that guard misses the case that actually cost a ten-hour outage: a build
// made by `make deploy` from a clean, committed worktree. Such a binary carries
// work no release contains, and it is what the running config was written
// against, so replacing it with a release both discards the work and can leave
// the config asking for a capability the release does not have. A config that
// fails validation drops every rule, so the whole gate goes open.
//
// Two shapes mean local. A version stamped by `git describe` gains a
// "-<commits>-g<sha>" suffix when the build is ahead of the last tag, which a
// release built at a tag never has. An unstamped build reports "dev" or
// "unknown". Either way the binary is not a release artifact.
func isLocalBuild(version string, dirty bool) bool {
	if dirty {
		return true
	}
	trimmed := strings.TrimSpace(version)
	if trimmed == "" || trimmed == "dev" || trimmed == "unknown" {
		return true
	}
	return hasGitDescribeSuffix(trimmed)
}

// hasGitDescribeSuffix reports whether version ends in git describe's
// "-<commits>-g<sha>" ahead-of-tag suffix, for example
// "202607281237-b5-e82aad0-9-g6586a88".
func hasGitDescribeSuffix(version string) bool {
	lastDash := strings.LastIndex(version, "-")
	if lastDash < 0 {
		return false
	}
	objectName := version[lastDash+1:]
	if len(objectName) < 2 || objectName[0] != 'g' {
		return false
	}
	if !isHex(objectName[1:]) {
		return false
	}
	countField := version[:lastDash]
	countDash := strings.LastIndex(countField, "-")
	if countDash < 0 {
		return false
	}
	return isDigits(countField[countDash+1:])
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func isHex(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		isDigit := character >= '0' && character <= '9'
		isLower := character >= 'a' && character <= 'f'
		isUpper := character >= 'A' && character <= 'F'
		if !isDigit && !isLower && !isUpper {
			return false
		}
	}
	return true
}
