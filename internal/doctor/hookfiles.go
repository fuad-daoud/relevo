package doctor

import (
	"fmt"
	"strings"
)

// The config doc carries the hook scripts themselves. The applier never
// overwrites a hand edit, so only `relevo doctor` reports the drift.

type HookFilesCheckInput struct {
	Carried int
	Edited  []EditedFile
}

// HookFilesCheck reports the hook scripts this machine kept rather than
// overwriting. It is the only place a kept hand edit is visible.
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
