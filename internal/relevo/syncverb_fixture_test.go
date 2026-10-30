package relevo

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	syncverb "github.com/fuad-daoud/relevo/internal/sync/verb"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// These fixtures build verb runners for the tests that stay in this package
// because they need the daemon or the config engine: the stop and daemon-guard
// tests below, the opener tests over config machines, and the unlock and fleet
// tests. They duplicate the same-named fixtures beside the verb tests in
// internal/sync/verb: a test helper cannot be imported across packages, and the
// verb package must not import this one. The values are identical on both
// sides, so a redaction grep for either token means the same fixture.

// verbFixtureToken is the token an enable is handed. Every redaction assertion
// greps for exactly this string.
const verbFixtureToken = "FIXTURE-TOKEN-8f3a2c91d4e7b605a1c2d3e4f5061728"

// verbFixtureR2 is the bucket configuration every verb fixture stores. The
// values are not credentials and reach no bucket: the transport under test is a
// record, and the only thing the daemon builds from them is the handshake.
var verbFixtureR2 = relevosync.R2Secrets{
	Endpoint: "https://acct.r2.cloudflarestorage.com",
	Bucket:   "relevo-sync",
	KeyID:    "key-1",
	Secret:   "secret-1",
}

const (
	joinPassphrase = "correct horse battery staple"
	joinToken      = "FIXTURE-JOIN-TOKEN-0123456789abcdef"
	joinRemote     = "libsql://fleet.invalid"
)

// verbFixture is one executor over a real split pair, plus the fake client and
// the opener it was handed.
type verbFixture struct {
	runner *syncverb.Runner
	shared *db.DB
	local  relevosync.Local
	client *recordTransport
	// opens is every transport a run asked for. Nothing opens one in this
	// build, so this is what a handle added back would be recorded in.
	opens int
}

// newVerbFixture builds an executor over a fresh split pair. The opener is the
// seam a remote would arrive through, and the fixture counts what asks for one.
func newVerbFixture(t *testing.T) *verbFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relevo.db")
	shared, err := db.OpenSplit(path, db.Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err := relevosync.LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	// A configured machine: a remote in the section and a token stored beside
	// it. A machine with neither is a different question and nothing on this
	// path answers it any more.
	putVerbSection(t, local, "libsql://example.invalid")
	if err := relevosync.SetToken(local, []byte(verbFixtureToken), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	// The bucket credentials too: an enable refuses a machine that holds none,
	// so a fixture about anything else has to be a configured machine. The values
	// reach no bucket -- the transport here is a record, not a worker.
	if err := relevosync.SetR2(local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	f := &recordTransport{}
	fx := &verbFixture{shared: shared, local: local, client: f}
	fx.runner = &syncverb.Runner{
		Shared: shared,
		Local:  local,
		Path:   shared.Path(),
		Runner: &relevosync.Runner{Client: f, Local: local},
		Open: func(context.Context) (synclog.LogTransport, error) {
			fx.opens++
			return f, nil
		},
		ClientName: "relevo",
	}
	return fx
}

// putVerbSection writes the machine-local sync section, which is where a remote
// lives. It is the row a hand-written `relevo config set sync` would write and
// the one a previous enable stored.
func putVerbSection(t *testing.T, local relevosync.Local, remote string) {
	t.Helper()
	if err := relevosync.PutSettings(local, relevosync.Settings{RemoteURL: remote}, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
}

// joinVerb is the frame a joiner sends: the code's remote and token, the
// passphrase, and the join flag.
func joinVerb(t *testing.T, pass string) *wire.SyncVerb {
	t.Helper()
	return &wire.SyncVerb{
		Verb:          wire.SyncVerbEnable,
		RemoteURL:     joinRemote,
		Join:          true,
		PassphraseLen: len(pass),
	}
}

func keyMetaFor(t *testing.T, pass string) synclog.KeyMeta {
	t.Helper()
	salt := []byte("0123456789abcdef")
	key := relevosync.DeriveKey([]byte(pass), salt, relevosync.DefaultKDF)
	return synclog.KeyMeta{
		Salt:    salt,
		KDF:     relevosync.DefaultKDF.String(),
		Check:   relevosync.KeyCheck(key),
		Present: true,
	}
}

// localState is every row C6 names, read as the bytes they are on disk. A refused
// join has to leave all four exactly as it found them, and comparing the bytes
// rather than the parsed values is what makes that the claim.
func localState(t *testing.T, l relevosync.Local) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows := []struct {
		name string
		read func() ([]byte, bool, error)
	}{
		{"section", func() ([]byte, bool, error) { return l.ConfigGet(relevosync.SectionSettings) }},
		{"token", func() ([]byte, bool, error) { return l.SecretGet(relevosync.SecretToken) }},
		{"sync.key", func() ([]byte, bool, error) { return l.SecretGet(relevosync.SecretKey) }},
		{"enabled", func() ([]byte, bool, error) { return l.KVGet(relevosync.KeyEnabled) }},
		{"remote_kdf", func() ([]byte, bool, error) { return l.KVGet(relevosync.KeyRemoteKDF) }},
	}
	for _, row := range rows {
		value, present, err := row.read()
		if err != nil {
			t.Fatalf("read %s: %v", row.name, err)
		}
		if present {
			out[row.name] = string(value)
			continue
		}
		out[row.name] = "<absent>"
	}
	return out
}

// stagedRows is localState plus every row the secrets document can land on and
// the join marker, which a refused join has to leave as it found them: the six
// allow-listed machine-local secrets so a refusal after the pre-pass cannot keep
// the fleet's values, and the replaced row an arriving client key that lands
// over a held one writes.
func stagedRows(t *testing.T, l relevosync.Local) map[string]string {
	t.Helper()
	out := localState(t, l)
	for _, name := range []string{
		relevosync.SecretClientKey, relevosync.SecretTypesafe,
		relevosync.SecretR2Endpoint, relevosync.SecretR2Bucket, relevosync.SecretR2KeyID, relevosync.SecretR2Secret,
	} {
		value, present, err := l.SecretGet(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out[name] = "<absent>"
		if present {
			out[name] = string(value)
		}
	}
	value, present, err := l.ConfigGet(config.ClientKeyReplacedKey)
	if err != nil {
		t.Fatalf("read %s: %v", config.ClientKeyReplacedKey, err)
	}
	out[config.ClientKeyReplacedKey] = "<absent>"
	if present {
		out[config.ClientKeyReplacedKey] = string(value)
	}
	value, present, err = l.KVGet(relevosync.KeyJoin)
	if err != nil {
		t.Fatalf("read %s: %v", relevosync.KeyJoin, err)
	}
	out[relevosync.KeyJoin] = "<absent>"
	if present {
		out[relevosync.KeyJoin] = string(value)
	}
	return out
}

func sameRows(t *testing.T, before, after map[string]string) {
	t.Helper()
	for name, want := range before {
		if after[name] != want {
			t.Errorf("%s changed across a refused join:\n before %q\n after  %q", name, want, after[name])
		}
	}
}
