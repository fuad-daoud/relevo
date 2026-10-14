package doctor

import (
	"fmt"
	"strings"
)

// The config doc carries the harness files a native agent names, and a person
// may edit one on disk afterwards. The applier never overwrites such a file --
// a hand edit is a person's edit -- so the drift between the document and the
// disk is something only `relevo doctor` reports: nothing else would ever
// mention it.
//
// The rendered half is the same rule seen from the other side. A source agent's
// definition is derived rather than carried, so there is nothing in the document
// to drift from; it drifts from the role manifest instead, and the re-render
// that follows an arriving item runs without Force, so a rendered file a person
// edited stays edited there too.

// EditedFile is one file on disk that no longer holds what it should: a native
// definition the document no longer matches, or a rendered one the manifest no
// longer vouches for.
type EditedFile struct {
	Path string
	Hash string
}

// AgentFilesCheckInput is the harness files this machine's config doc carries,
// the ones a person has edited since, and the rendered definitions of the
// config's own agents that a person has edited too.
type AgentFilesCheckInput struct {
	Carried  int
	Edited   []EditedFile
	Rendered []EditedFile
}

// AgentFilesCheck reports the agent definitions this machine kept rather than
// overwriting, one line naming each, with the command that puts the right copy
// back.
//
// The check is the only place a kept hand edit is visible: the applier keeps it
// silently so a peer's other agents still travel, and the re-render keeps it
// without Force so the same is true of a rendered one.
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
