package relevo

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainResumeRaiseCaps lifts each local member's round cap to the one the
// resumed settings imply. A cap is never lowered, so a hand-set higher one
// survives. Served and remote members keep the cap fixed at create.
func chainResumeRaiseCaps(tx *store.Tx, c db.ChainRow, plans int, set chain.Settings) error {
	rows, err := chainFlowMembers(tx, c)
	if err != nil {
		return err
	}
	for _, m := range rows {
		b, err := tx.Load(m.Binding)
		if err != nil {
			return err
		}
		if b.Owner != "" || b.Builder.Remote() {
			continue
		}
		want := chainMemberRoundCap(chain.PartOf(c, b.Name), plans, set)
		if want <= b.RoundCap {
			continue
		}
		b.RoundCap = want
		if err := tx.Save(b); err != nil {
			return fmt.Errorf("raise round cap of %s: %w", b.Name, err)
		}
	}
	return nil
}
