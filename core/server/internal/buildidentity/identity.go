package buildidentity

import "runtime/debug"

// Missing VCS stamping must stay unknown; the sing-box dependency revision is
// not the ThroneCore source revision.
func Revision(info *debug.BuildInfo) (commit, modified string) {
	commit, modified = "unknown", "unknown"
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
