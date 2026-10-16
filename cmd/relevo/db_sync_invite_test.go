package main

// The invite verb's CLI shape. Like the rest of this file's tests these parse
// flags, read the usage text and walk the dispatch: nothing here opens a
// database, dials an owner or reaches a network. The rule invite implements
// against the rows is covered where it lives, in internal/relevo.

import (
	"encoding/json"
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	"github.com/fuad-daoud/relevo/internal/relevo"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// The usage text names invite and says what the code carries. A reader who
// pastes a code into a ticket deserves to have been told, on the same screen,
// that it holds a bearer credential.
func TestInviteIsInTheUsageAndSaysWhatTheCodeCarries(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"relevo db sync invite", "turso.token"} {
		if !strings.Contains(dbSyncUsage, want) {
			t.Errorf("the usage text does not mention %q:\n%s", want, dbSyncUsage)
		}
	}
}

// The dispatcher routes invite to the verb. A verb wired into the usage but not
// into the dispatch table is one that cannot be run.
func TestInviteIsDispatched(t *testing.T) {
	t.Parallel()
	// A bad flag is the cheapest way to prove the dispatcher reached the verb: the
	// verb's own flag set rejects it, while the bare dispatcher's does not know
	// the flag at all and prints usage instead.
	err := cmdDBSync([]string{"invite", "--not-a-flag"})
	if !errors.Is(err, flag.ErrHelp) && err == nil {
		t.Fatalf("`db sync invite --not-a-flag` = %v, want the verb's own flag parser to answer", err)
	}
	if errors.Is(err, errUsagePrinted) {
		t.Errorf("`db sync invite --not-a-flag` fell through to the bare usage: %v", err)
	}
}

// The flag set is --json and nothing else, so a mistyped flag is caught at the
// point of the typo rather than ignored by a parser that does not know it.
func TestInviteTakesOnlyJSON(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("db sync invite", flag.ContinueOnError)
	fs.SetOutput(discardWriter{})
	v := dbSyncInviteFlagSet(fs)
	if err := fs.Parse([]string{"--json"}); err != nil {
		t.Fatalf("parse --json: %v", err)
	}
	if !*v.asJSON {
		t.Error("--json did not set the document flag")
	}
	if err := fs.Parse([]string{"--url", "libsql://x.invalid"}); err == nil {
		t.Error("invite accepted --url, which names a remote only the inviter has")
	}
}

// The two refusals a reader can act on name the verb that answers them. Without
// the hint the messages would describe a state the reader cannot change.
func TestInviteRefusalsNameTheVerbThatFixesThem(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"not syncing", relevosync.ErrInviteSyncOff, "relevo db sync enable"},
		{"the remote holds no key", relevosync.ErrNoKDF, "relevo db sync unlock --new"},
	} {
		classified := dbSyncInviteClassify(tc.err)
		var got *cliError
		if !errors.As(classified, &got) {
			t.Errorf("%s: classified = %v, want a CLI error", tc.name, classified)
			continue
		}
		if got.code != codeRefused {
			t.Errorf("%s: code = %v, want a refusal", tc.name, got.code)
		}
		if !strings.Contains(classified.Error(), tc.want) {
			t.Errorf("%s: %q does not name %q", tc.name, classified.Error(), tc.want)
		}
	}
}

// A code this build cannot read is the reader's mistake, not a defect report.
func TestInviteRefusesAnUnreadableCodeAsUsage(t *testing.T) {
	t.Parallel()
	var got *cliError
	if err := dbSyncInviteClassify(relevosync.ErrJoinCode); !errors.As(err, &got) || got.code != codeUsage {
		t.Fatalf("classified = %v, want a usage error", err)
	}
}

// An invite refusal is not something to send to `relevo bugreport`, and neither
// is the wire's no-kdf class when it reaches an enable.
func TestInviteNoKDFIsNotADefectReport(t *testing.T) {
	t.Parallel()
	var got *cliError
	classified := dbSyncVerbRefusal(wire.SyncVerbEnable, &client.VerbRefusal{
		Code:    wire.SyncCodeNoKDF,
		Message: "sync: the remote holds no key yet",
	})
	if !errors.As(classified, &got) || got.code != codeRefused {
		t.Fatalf("classified = %v, want a refusal", classified)
	}
}

// A locked machine says so beside the code, and an unlocked, current one does
// not. A stale machine says its key is old and names the unlock that fixes it,
// which is one more line and not a replacement for the locked one -- the two
// states are apart and both are worth a line each. The line is unconditional on
// the one machine it is true for -- a reader holding a code from a locked
// inviter would otherwise assume it carries that machine's secrets -- and
// absent everywhere else, because a warning that is always there is a warning
// nobody reads.
func TestInviteSaysWhenThisMachineIsLocked(t *testing.T) {
	t.Parallel()
	notes := dbSyncInviteNotes(relevo.Invite{Locked: true})
	if len(notes) != 1 || !strings.Contains(notes[0], "locked") {
		t.Fatalf("a locked machine prints notes %q, want one line saying it is locked", notes)
	}
	if got := dbSyncInviteNotes(relevo.Invite{}); len(got) != 0 {
		t.Errorf("an unlocked machine prints notes %q, want none", got)
	}
	// A stale machine adds the one line that names the act that unblocks it,
	// whether or not it is also locked -- a locked machine holds no epoch, so
	// the two are apart in practice, and the rule does not guess.
	if got := dbSyncInviteNotes(relevo.Invite{Stale: true}); len(got) != 1 ||
		!strings.Contains(got[0], "holds an old key") ||
		!strings.Contains(got[0], "relevo db sync unlock") {
		t.Errorf("a stale machine prints notes %q, want the one line naming the unlock", got)
	}
}

// The document says the same thing the prose does, as a field. Without it a
// caller reading --json would have to parse the stderr line to learn that the
// code it just received came from a machine that had not shared its secrets.
func TestInviteJSONCarriesTheLockedFact(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(dbSyncInviteDoc{Code: "relevo-join-v1-x", Locked: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Locked *bool `json:"locked"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Locked == nil || !*decoded.Locked {
		t.Errorf("the document %s does not carry locked=true", body)
	}
}

// discardWriter is where a flag set's output goes in these tests. A usage line
// printed by a rejected flag is the parser telling the caller what happened, and
// the test asserts on the error rather than on that line.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
