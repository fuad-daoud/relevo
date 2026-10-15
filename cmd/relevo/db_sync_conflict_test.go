package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// TestRemoteConflictRefusalNamesTheRepointCommand pins that both routes a
// remote conflict reaches the CLI by -- a direct classify and a verb refusal --
// carry the config command that re-points the machine at the --url value.
func TestRemoteConflictRefusalNamesTheRepointCommand(t *testing.T) {
	t.Parallel()
	const want = "relevo config set sync.remote_url libsql://flag.invalid"
	direct := dbSyncClassify(&relevosync.RemoteConflictError{URL: "libsql://flag.invalid", Stored: "libsql://stored.invalid"})
	viaVerb := dbSyncVerbRefusalFor(wire.SyncVerbEnable, "libsql://flag.invalid", &client.VerbRefusal{
		Code: wire.SyncCodeRemoteConflict, Message: "sync: conflict",
	})
	for name, err := range map[string]error{"direct": direct, "verb": viaVerb} {
		var got *cliError
		if !errors.As(err, &got) {
			t.Fatalf("%s: %v is not a cliError", name, err)
		}
		if got.next != want {
			t.Errorf("%s: next = %q, want %q", name, got.next, want)
		}
		if got.code != codeRefused {
			t.Errorf("%s: code = %q, want refused", name, got.code)
		}
	}
	if !strings.Contains(direct.Error(), "libsql://stored.invalid") {
		t.Errorf("direct refusal %q does not name the stored remote", direct)
	}
}
