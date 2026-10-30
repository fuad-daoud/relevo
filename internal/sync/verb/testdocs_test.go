package verb

import (
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The verb test binary does not link internal/relevo, so its init -- which
// registers the production documents -- never runs. Without a registered
// builder the pipeline moves no document, and every test that reaches the
// fleet through one would pin an empty pipeline rather than the verb: a join's
// bucket credentials ride the secrets document.
//
// These tests register the secrets document alone, built the same way
// production builds it. The full assembly -- config, gates and consent beside
// secrets -- stays pinned where it lives, by the config and opener tests in
// internal/relevo, which link the production registration.
func init() {
	relevosync.SetDocsBuilder(testDocs)
}

func testDocs(shared *db.DB) []synclog.SharedDoc {
	return []synclog.SharedDoc{config.Open(shared).SecretsDoc()}
}
