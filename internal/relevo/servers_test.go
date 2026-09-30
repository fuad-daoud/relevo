package relevo

import (
	"context"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
)

// TestProbeServersAudienceRefusalsAreErrors pins that the two audience
// sentinels do not read as "not enrolled": they are plain errors, so the probe
// reports the text and a retry never loops on them.
func TestProbeServersAudienceRefusalsAreErrors(t *testing.T) {
	t.Parallel()

	servers := map[string]remote.ServerEntry{"zen": {URL: "https://zen:7777"}}

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"server too old", client.ErrServerTooOld},
		{"wrong audience", client.ErrWrongAudience},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := Runtime{Remote: &fakeRemote{whoAmIErr: tc.err}}
			probes := ProbeServers(context.Background(), rt, servers, "enroll me")
			if len(probes) != 1 {
				t.Fatalf("got %d probes, want 1", len(probes))
			}
			p := probes[0]
			if p.State != "error" {
				t.Fatalf("State = %q, want error", p.State)
			}
			if p.Detail != tc.err.Error() {
				t.Fatalf("Detail = %q, want %q", p.Detail, tc.err.Error())
			}
		})
	}
}
