package relevo

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/view"
)

// ChainStatus is `relevo status <chain>`: the chain's own row with its member
// bindings listed underneath, builder first. The members keep their ordinary
// rows here whatever the chain's status -- naming a chain asks for the whole
// chain, not only the roll-up.
func ChainStatus(ctx context.Context, rt Runtime, name string) (view.Report, error) {
	c, err := rt.Store.Chain(name)
	if err != nil {
		return view.Report{}, err
	}
	// A chain that runs on a server answers from the server's view, so the
	// status a human reads is the chain's, not this machine's stale mirror.
	if chain.OnServer(c) {
		return chainServerStatus(ctx, rt, c)
	}
	bindings, err := rt.Store.List()
	if err != nil {
		return view.Report{}, err
	}
	rep, err := buildReport(ctx, rt, bindings)
	if err != nil {
		return view.Report{}, err
	}

	rows := []view.BindingStatus{view.ChainRow(rt.Store, c)}
	for _, member := range view.ReadMembers(rt.Store, c) {
		for _, b := range rep.Bindings {
			if b.Name == member {
				rows = append(rows, b)
				break
			}
		}
	}
	rep.Bindings = rows
	return rep, nil
}
