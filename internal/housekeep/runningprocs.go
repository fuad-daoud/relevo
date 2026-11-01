package housekeep

import "github.com/fuad-daoud/relevo/internal/store"

// RunningProcRef is one local process a daemon restart could kill, as a plain
// tuple. The collector reads only the binding model; cmd/relevo converts the
// tuples to doctor.RunningProc, so neither internal/housekeep nor
// internal/doctor imports the other.
type RunningProcRef struct {
	Binding string
	Kind    string // "builder", "gate" or "consult"
	PID     int
}

// RunningProcs collects the processes bs says are running locally: only local
// bindings with a pid -- a headless builder, the current round's gate, and
// every running consult with a headless endpoint. Pure: it reads bs and
// nothing else.
//
// A pane builder and an adopted endpoint are deliberately absent: relevo did
// not start them, so a daemon restart does not affect them.
func RunningProcs(bs []store.Binding) []RunningProcRef {
	var out []RunningProcRef
	for _, b := range bs {
		if b.Owner != "" || b.Builder.Remote() {
			continue
		}
		if b.Builder.Headless() && b.Builder.PID > 0 {
			out = append(out, RunningProcRef{Binding: b.Name, Kind: "builder", PID: b.Builder.PID})
		}
		if b.GateRun != nil && b.GateRun.PID > 0 {
			out = append(out, RunningProcRef{Binding: b.Name, Kind: "gate", PID: b.GateRun.PID})
		}
		for _, c := range b.Consults {
			if c.State == store.ConsultRunning && c.Endpoint.Headless() && c.Endpoint.PID > 0 {
				out = append(out, RunningProcRef{Binding: b.Name, Kind: "consult", PID: c.Endpoint.PID})
			}
		}
	}
	return out
}
