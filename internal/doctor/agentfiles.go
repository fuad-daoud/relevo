package doctor

import (
	"fmt"
	"strings"
)

// The config doc carries the harness files a native agent names. The applier
// never overwrites a hand edit, so only `relevo doctor` reports the drift --
// including the rendered half, which drifts from the role manifest instead.

// EditedFile is one file on disk that no longer holds what it should: a native
// definition the document no longer matches, or a rendered one the manifest no
// longer vouches for.
type EditedFile struct {
	Path string
	Hash string
}

type AgentFilesCheckInput struct {
	Carried  int
	Edited   []EditedFile
	Rendered []EditedFile
}

// AgentFilesCheck reports the agent definitions this machine kept rather
// than overwriting. It is the only place a kept hand edit is visible.
func AgentFilesCheck(in AgentFilesCheckInput) Check {
	edited := in.Edited
	carried := in.Carried
	if len(in.Rendered) > 0 {
		edited = append(append([]EditedFile(nil), edited...), in.Rendered...)
		carried += len(in.Rendered)
	}
	switch {
	case carried == 0:
		return Check{Name: "agent-files", Detail: "agent-files: no files carried", Severity: SevOK}
	case len(edited) == 0:
		return Check{
			Name:     "agent-files",
			Detail:   fmt.Sprintf("agent-files: %d files carried, none edited on disk", carried),
			Severity: SevOK,
		}
	}
	names := make([]string, 0, len(edited))
	for _, file := range edited {
		names = append(names, file.Path)
	}
	return Check{
		Name:     "agent-files",
		Detail:   fmt.Sprintf("agent-files: %d of %d files edited on disk and kept: %s", len(edited), carried, strings.Join(names, ", ")),
		Fix:      "relevo config agents --force",
		Severity: SevWarn,
	}
}
