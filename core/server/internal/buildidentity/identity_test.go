package buildidentity

import (
	"runtime/debug"
	"testing"
)

func TestRevision(t *testing.T) {
	for _, info := range []*debug.BuildInfo{nil, {}, {Settings: []debug.BuildSetting{{Key: "vcs", Value: "git"}}}} {
		commit, dirty := Revision(info)
		if commit != "unknown" || dirty != "unknown" {
			t.Fatal(commit, dirty)
		}
	}
	info := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}, {Key: "vcs.modified", Value: "true"}}}
	commit, dirty := Revision(info)
	if commit != "abc123" || dirty != "true" {
		t.Fatal(commit, dirty)
	}
}
