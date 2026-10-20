package buildidentity

import (
	"fmt"
	"regexp"
	"runtime/debug"
	"strings"
)

// SourceCommit and SourceModified are stamped by the builder from the selected
// source checkout BEFORE copying/generated files. An enclosing builder's Git
// metadata is not the Core source identity.
var SourceCommit string
var SourceModified string

// Missing VCS stamping must stay unknown; the sing-box dependency revision is
// not the ThroneCore source revision.
func Revision(info *debug.BuildInfo) (commit, modified string) {
	commit, modified = "unknown", "unknown"
	if SourceCommit != "" || SourceModified != "" {
		if regexp.MustCompile(`^[0-9a-fA-F]{40}$`).MatchString(SourceCommit) {
			commit = strings.ToLower(SourceCommit)
		}
		if SourceModified == "true" || SourceModified == "false" {
			modified = SourceModified
		}
		return
	}
	if info == nil {
		return
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if setting.Value != "" {
				commit = setting.Value
			}
		case "vcs.modified":
			if setting.Value == "true" || setting.Value == "false" {
				modified = setting.Value
			}
		}
	}
	return
}

// Report is also consumed by the builder to verify each executable before it is
// packaged. It runs without starting RPC, DNS, TUN or the parent watcher.
func Report(info *debug.BuildInfo) string {
	commit, modified := Revision(info)
	return fmt.Sprintf("CORE_BUILD_IDENTITY commit=%s modified=%s", commit, modified)
}
