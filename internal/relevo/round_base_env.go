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

// chainBaseEnvEntry is the same entry for a chain check's gate process: a
// check has no binding, so the chain row's base stands in for the binding's,
// and the shape must stay identical to baseEnvEntry's because the same diff
// scripts read the name from either process. An empty base returns nil.
func chainBaseEnvEntry(base string) []string {
	if base == "" {
		return nil
	}
	return []string{baseEnvName + "=" + base}
}
