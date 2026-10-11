package relevo

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// TestHaltPayloadNamesBothWaysOut pins the halt payload's two hints: a resume
// once the cause is fixed, and `relevo done` for a reader who has given up on
// the work. The mutation it kills is dropping the done clause, which leaves a
// payload naming only the resume.
func TestHaltPayloadNamesBothWaysOut(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		build func() string
	}{
		{"workflow", func() string {
			return chainTerminalWFPayload(
				db.ChainRow{Name: "shop", Branch: "relevo/shop"},
				workflow.State{Status: workflow.StatusHalted, At: "repair"})
		}},
		{"legacy", func() string {
			return chainTerminalPayload(
				db.ChainRow{Name: "shop", Branch: "relevo/shop", Status: string(chain.StatusHalted)}, 0)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.build()
			for _, want := range []string{
				"relevo chain --resume --name shop",
				"`relevo done shop` closes it",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("halt payload %q does not carry %q", got, want)
				}
			}
		})
	}
}

// TestEndPayloadWithoutAHaltNamesNeither pins the other side of the same rule:
// a chain that finished names neither command, because there is nothing to
// resume and nothing to close.
func TestEndPayloadWithoutAHaltNamesNeither(t *testing.T) {
	t.Parallel()

	got := chainTerminalWFPayload(
		db.ChainRow{Name: "shop", Branch: "relevo/shop"},
		workflow.State{Status: workflow.StatusDone, At: "review"})
	for _, unwanted := range []string{"--resume", "relevo done"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("a finished chain's payload %q carries %q", got, unwanted)
		}
	}
}
