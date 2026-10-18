package relevo

import "github.com/fuad-daoud/relevo/internal/store"

const baseEnvName = "RELEVO_BASE"

// baseEnvEntry is the entry scripts read to diff against the commit the round
// was cut from, because a served tree has no origin/main.
func baseEnvEntry(b store.Binding) []string {
	if b.Base == "" {
		return nil
	}
	return []string{baseEnvName + "=" + b.Base}
}
