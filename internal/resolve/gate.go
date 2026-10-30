package resolve

// The words that name the tier a resolved gate came from: the printed gate and
// check lines carry one in parentheses, so a human can tell a flag's own
// command from the repository's or the policy's.
const (
	GateSourceFlag    = "--gate"
	GateSourceRepo    = "repo setting"
	GateSourcePolicy  = "policy.gate.default"
	GateSourceBinding = "binding"
)

// GateResolution is the gate command a bind, an add or a chain start resolved,
// and the tier that supplied it. The zero value means no gate: a bind with no
// command to run prints "gate: none" whatever tier the absence came from.
type GateResolution struct {
	Command string
	Source  string
}
