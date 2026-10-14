package doctor

import (
	"fmt"
	"strings"
)

// The config doc carries the hook scripts themselves, and a person may edit
// one on disk afterwards. The applier never overwrites such a file -- a hand
// edit is a person's edit -- so the drift between the document and the disk is
// something only `relevo doctor` reports: nothing else would ever mention it.

// HookFilesCheckInput is the scripts this machine's config doc carries and the
// ones a person has edited since, as the config store reports them.
type HookFilesCheckInput struct {
	Carried int
	Edited  []EditedFile
}

// HookFilesCheck reports the hook scripts this machine kept rather than
// overwriting, one line naming each, with the command that puts the document's
// copy back.
//
// The check is the only place a kept hand edit is visible: the applier keeps it
// silently so a peer's other scripts still travel.
func HookFilesCheck(in HookFilesCheckInput) Check {
	if in.Carried == 0 && len(in.Edited) == 0 {
		return Check{Name: "hook-files", Detail: "hook-files: no scripts carried", Severity: SevOK}
	}
	if len(in.Edited) == 0 {
		return Check{
			Name:     "hook-files",
			Detail:   fmt.Sprintf("hook-files: %d scripts carried, none edited on disk", in.Carried),
			Severity: SevOK,
		}
	}
	names := make([]string, 0, len(in.Edited))
	for _, file := range in.Edited {
		names = append(names, file.Path)
	}
	return Check{
		Name:     "hook-files",
		Detail:   fmt.Sprintf("hook-files: %d of %d scripts edited on disk and kept: %s", len(in.Edited), in.Carried, strings.Join(names, ", ")),
		Fix:      "relevo config hooks --restore",
		Severity: SevWarn,
	}
}
