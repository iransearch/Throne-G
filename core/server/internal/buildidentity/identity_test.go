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

func TestSelectedSourceOverridesBuilderRepository(t *testing.T) {
	oldCommit, oldModified := SourceCommit, SourceModified
	defer func() { SourceCommit, SourceModified = oldCommit, oldModified }()
	SourceCommit = "51aa2be81c210d72adf2e41b86677e743cf755ac"
	SourceModified = "false"
	builder := &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "edd0d9cba228e015c70f8959fac4f8334d8a170c"},
		{Key: "vcs.modified", Value: "true"},
	}}
	commit, modified := Revision(builder)
	if commit != SourceCommit || modified != "false" {
		t.Fatal(commit, modified)
	}
	SourceModified = "true"
	commit, modified = Revision(nil)
	if commit != SourceCommit || modified != "true" {
		t.Fatal(commit, modified)
	}
	SourceCommit, SourceModified = "invalid", "invalid"
	commit, modified = Revision(builder)
	if commit != "unknown" || modified != "unknown" {
		t.Fatal("invalid stamp fell back to foreign repository", commit, modified)
	}
}
