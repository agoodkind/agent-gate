package updateopts

import (
	"testing"

	gkversion "goodkind.io/gklog/version"
)

// The two version strings from the 2026-07-31 outage. The installed binary was
// the local one; the updater replaced it with the release one, which could not
// load the config written for the local build, so the daemon crash-looped for
// ten hours with every rule dropped.
const (
	localBuildVersion   = "202607281237-b5-e82aad0-9-g6586a88"
	releaseBuildVersion = "202607301700-b6-700f20b"
)

// TestLocalDeployIsNeverAutoReplaced is the regression: the exact build that
// was overwritten must now report as a local build, and the release that
// overwrote it must not, so a real release still installs normally.
func TestLocalDeployIsNeverAutoReplaced(t *testing.T) {
	if !isLocalBuild(localBuildVersion, false) {
		t.Fatalf("isLocalBuild(%q, dirty=false) = false, want true", localBuildVersion)
	}
	if isLocalBuild(releaseBuildVersion, false) {
		t.Fatalf("isLocalBuild(%q, dirty=false) = true, want false", releaseBuildVersion)
	}
}

func TestIsLocalBuild(t *testing.T) {
	cases := []struct {
		name    string
		version string
		dirty   bool
		want    bool
	}{
		{"release tag", releaseBuildVersion, false, false},
		{"release tag built dirty", releaseBuildVersion, true, true},
		{"clean build ahead of a tag", localBuildVersion, false, true},
		{"one commit ahead", "202607301700-b6-700f20b-1-gabc1234", false, true},
		{"unstamped dev", "dev", false, true},
		{"unstamped unknown", "unknown", false, true},
		{"empty", "", false, true},
		{"semver release", "v1.4.2", false, false},
		{"semver ahead of tag", "v1.4.2-3-gdeadbee", false, true},
		// A trailing field that only looks like a describe suffix must not be
		// mistaken for one, or real releases stop updating.
		{"trailing g without hex", "202607301700-b6-gzzzz", false, false},
		{"trailing hex without commit count", "202607301700-b6-700f20b-gabc1234", false, false},
		{"count field is not a number", "202607301700-b6-x-gabc1234", false, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := isLocalBuild(testCase.version, testCase.dirty)
			if got != testCase.want {
				t.Fatalf("isLocalBuild(%q, %v) = %v, want %v",
					testCase.version, testCase.dirty, got, testCase.want)
			}
		})
	}
}

// TestOptionsCarriesLocalBuildIntoTheUpdaterGuard covers the wiring rather than
// the predicate: whatever isLocalBuild decides has to arrive in the field the
// updater actually reads, or the guard is inert.
func TestOptionsCarriesLocalBuildIntoTheUpdaterGuard(t *testing.T) {
	options := Options(nil, Overrides{})
	want := isLocalBuild(gkversion.Version, gkversion.Dirty == "true")
	if options.Config.CurrentDirty != want {
		t.Fatalf("Options CurrentDirty = %v, want %v", options.Config.CurrentDirty, want)
	}
}
