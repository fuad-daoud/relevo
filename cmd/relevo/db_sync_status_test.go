//go:build unix

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// The gap this file closes: `db sync status` shared openDBSync with the four
// verbs that write, so it opened the machine database directly and refused with
// `conflict` while the daemon ran -- for a verb that reads three local rows and
// writes nothing. Every test here drives the real verb against a real split
// pair served by a real owner, so the bytes on stdout are the ones a running
// daemon produces. None of them reaches a network or starts a harness.
//
// Two shapes of "the daemon is running" appear below, and the difference
// matters. An in-process owner (serveSyncStatus) is the same process, so a
// direct open of the file it holds shares the one open lock this package keeps
// per path and succeeds -- which is why the writers' conflict cannot be
// reproduced that way. A daemon in a child process (startSyncStatusDaemon) is
// the only shape that takes the lock for real, so that is where the conflict
// and the daemon's own answers are pinned.

// The fixture a sync section is seeded with. The remote is an unroutable name:
// these tests open no handle to it, and a name that could resolve would only
// invite one.
const (
	syncStatusRemote    = "libsql://sync-status-fixture.invalid"
	syncStatusNamespace = "default"
	syncStatusSecret    = "sync-status-token-fixture"
)

// syncStatusNow is the timestamp every marker is stamped with, so a seeded file
// is byte-identical between the two roots a case is compared across.
var syncStatusNow = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

// dbSyncStatusCase is one machine's sync state: the local markers that produce
// it, the statusline token those same markers must map to, the exact line
// `db sync status` must print, and the exact document it must print under
// --json. The last two are the golden: the line is the verb's whole output and
// the document is what an agent reads, so both are pinned byte for byte rather
// than compared against whatever the current renderer happens to produce.
type dbSyncStatusCase struct {
	name      string
	enabled   bool
	tickOK    bool
	attention string
	backlog   int64
	remote    string
	namespace string
	token     string
	wantToken string
	wantLine  string
	wantDoc   string
}

// dbSyncStatusCases covers every state the statusline has a token for -- off,
// on, behind and err -- and each with and without a backlog, because the
// backlog is the one marker that moves a machine between two of them while the
// enable and the remote stay exactly where they were.
var dbSyncStatusCases = []dbSyncStatusCase{
	{
		name:      "off, never configured",
		wantToken: relevosync.TokenOff,
		wantLine:  "sync off (remote: (none), token: absent)\n",
		wantDoc:   "{\n  \"enabled\": false,\n  \"token_present\": false\n}\n",
	},
	{
		name:      "off, a remote configured but sync never turned on",
		remote:    syncStatusRemote,
		namespace: syncStatusNamespace,
		token:     syncStatusSecret,
		wantToken: relevosync.TokenOff,
		wantLine:  "sync off (remote: " + syncStatusRemote + ", token: present)\n",
		wantDoc:   "{\n  \"enabled\": false,\n  \"remote_url\": \"" + syncStatusRemote + "\",\n  \"namespace\": \"" + syncStatusNamespace + "\",\n  \"token_present\": true\n}\n",
	},
	{
		name:      "on, no backlog",
		enabled:   true,
		tickOK:    true,
		remote:    syncStatusRemote,
		namespace: syncStatusNamespace,
		token:     syncStatusSecret,
		wantToken: relevosync.TokenOK,
		wantLine:  "sync on (remote: " + syncStatusRemote + ", token: present)\n",
		wantDoc:   "{\n  \"enabled\": true,\n  \"remote_url\": \"" + syncStatusRemote + "\",\n  \"namespace\": \"" + syncStatusNamespace + "\",\n  \"token_present\": true\n}\n",
	},
	{
		name:      "on, a backlog at the threshold",
		enabled:   true,
		tickOK:    true,
		backlog:   relevosync.BacklogThreshold,
		remote:    syncStatusRemote,
		namespace: syncStatusNamespace,
		token:     syncStatusSecret,
		wantToken: relevosync.TokenOK,
		wantLine:  "sync on (remote: " + syncStatusRemote + ", token: present)\n",
		wantDoc:   "{\n  \"enabled\": true,\n  \"remote_url\": \"" + syncStatusRemote + "\",\n  \"namespace\": \"" + syncStatusNamespace + "\",\n  \"token_present\": true\n}\n",
	},
	{
		name:      "on, a backlog over the threshold",
		enabled:   true,
		tickOK:    true,
		backlog:   relevosync.BacklogThreshold + 1,
		remote:    syncStatusRemote,
		namespace: syncStatusNamespace,
		token:     syncStatusSecret,
		wantToken: relevosync.TokenBehind,
		wantLine:  "sync on (remote: " + syncStatusRemote + ", token: present)\n",
		wantDoc:   "{\n  \"enabled\": true,\n  \"remote_url\": \"" + syncStatusRemote + "\",\n  \"namespace\": \"" + syncStatusNamespace + "\",\n  \"token_present\": true\n}\n",
	},
	{
		name:      "on, a tick that failed with no backlog",
		enabled:   true,
		remote:    syncStatusRemote,
		namespace: syncStatusNamespace,
		token:     syncStatusSecret,
		wantToken: relevosync.TokenBehind,
		wantLine:  "sync on (remote: " + syncStatusRemote + ", token: present)\n",
		wantDoc:   "{\n  \"enabled\": true,\n  \"remote_url\": \"" + syncStatusRemote + "\",\n  \"namespace\": \"" + syncStatusNamespace + "\",\n  \"token_present\": true\n}\n",
	},
	{
		name:      "on, an error a human must act on",
		enabled:   true,
		tickOK:    true,
		attention: "the remote refused the stored credential",
		remote:    syncStatusRemote,
		namespace: syncStatusNamespace,
		token:     syncStatusSecret,
		wantToken: relevosync.TokenErr,
		wantLine:  "sync on (remote: " + syncStatusRemote + ", token: present)\n",
		wantDoc:   "{\n  \"enabled\": true,\n  \"remote_url\": \"" + syncStatusRemote + "\",\n  \"namespace\": \"" + syncStatusNamespace + "\",\n  \"token_present\": true\n}\n",
	},
	{
		name:      "off, with an attention marker still set",
		attention: "the remote refused the stored credential",
		wantToken: relevosync.TokenOff,
		wantLine:  "sync off (remote: (none), token: absent)\n",
		wantDoc:   "{\n  \"enabled\": false,\n  \"token_present\": false\n}\n",
	},
}

// seedSyncStatusLocal writes the local markers one case describes, the way an
// enable and a measured tick leave them. Every row goes to the machine-local
// file: there is nowhere else a sync row is allowed to live, which is what makes
// the refusal an old owner owes its client the only thing worth asserting.
func seedSyncStatusLocal(t *testing.T, local *db.DB, tc dbSyncStatusCase) {
	t.Helper()
	if tc.remote != "" {
		body, err := json.Marshal(relevosync.Settings{
			Enabled:   tc.enabled,
			RemoteURL: tc.remote,
			Namespace: tc.namespace,
		})
		if err != nil {
			t.Fatalf("encode the sync section: %v", err)
		}
		if err := local.Tx(func(tx *db.Tx) error {
			return tx.ConfigPut(relevosync.SectionSettings, body, syncStatusNow)
		}); err != nil {
			t.Fatalf("write the sync section: %v", err)
		}
	}
	if err := relevosync.MarkEnabled(local, tc.enabled, syncStatusNow); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	if tc.token != "" {
		if err := relevosync.SetToken(local, []byte(tc.token), syncStatusNow); err != nil {
			t.Fatalf("SetToken: %v", err)
		}
	}
	if tc.enabled {
		ok := "false"
		if tc.tickOK {
			ok = "true"
		}
		tick := []byte(`{"at":"2026-10-04T12:00:00Z","ok":` + ok + `}`)
		if err := local.KVPut(relevosync.KeyLastTick, tick); err != nil {
			t.Fatalf("write the tick marker: %v", err)
		}
	}
	if tc.backlog != 0 {
		body := []byte(strconv.FormatInt(tc.backlog, 10))
		if err := local.KVPut(relevosync.KeyBacklog, body); err != nil {
			t.Fatalf("write the backlog marker: %v", err)
		}
	}
	if tc.attention != "" {
		att := []byte(`{"at":"2026-10-04T12:00:00Z","message":"` + tc.attention + `"}`)
		if err := local.KVPut(relevosync.KeyAttention, att); err != nil {
			t.Fatalf("write the attention marker: %v", err)
		}
	}
}

// serveSyncStatus opens a split pair on a fresh state root, seeds it, and serves
// it on the root's socket -- the daemon's own shape, in this process. It points
// XDG_STATE_HOME at that root, so the verb under test resolves the same path the
// owner serves.
func serveSyncStatus(t *testing.T, tc dbSyncStatusCase) {
	t.Helper()
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	served := startTestOwner(t, root)
	seedSyncStatusLocal(t, served.db.Local(), tc)
}

// directSyncStatus opens the same shape with no owner behind it and seeds it
// identically: the handle the verb used before it was routed through the owner.
func directSyncStatus(t *testing.T, tc dbSyncStatusCase) *db.DB {
	t.Helper()
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	d, err := openDBDirect(machineDBPath())
	if err != nil {
		t.Fatalf("openDBDirect: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	seedSyncStatusLocal(t, d.Local(), tc)
	return d.Local()
}

// readSyncStatusDoc is the three reads the verb makes, off whichever local
// handle it is handed. It is the whole of what the route can move: same rows
// out, same document.
func readSyncStatusDoc(t *testing.T, local *db.DB) dbSyncStatusDoc {
	t.Helper()
	settings, err := relevosync.ReadSettings(local)
	if err != nil {
		t.Fatalf("ReadSettings: %v", err)
	}
	_, hasToken, err := relevosync.ReadToken(local)
	if err != nil {
		t.Fatalf("ReadToken: %v", err)
	}
	on, err := relevosync.Enabled(local)
	if err != nil {
		t.Fatalf("Enabled: %v", err)
	}
	return dbSyncStatusDoc{
		Enabled:      on,
		RemoteURL:    settings.RemoteURL,
		Namespace:    settings.Namespace,
		TokenPresent: hasToken,
	}
}

// dialledLocal dials the owner the current environment's root serves and returns
// its machine-local file -- the handle the routed verb reads through.
func dialledLocal(t *testing.T) *db.DB {
	t.Helper()
	d, err := dialOwner(t.Context(), machineRoot(t), verbDialBudget)
	if err != nil {
		t.Fatalf("dialOwner: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	local, err := relevosync.LocalHandle(d)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	return local
}

// TestDBSyncStatusOverTheOwnerRoutePrintsTheDirectOpenBytes pins the whole
// promise of this change: for every state the statusline has a token for, the
// verb dialling the owner prints exactly the bytes the same state prints through
// a direct open, and the same document under --json. The two roots are seeded
// from the same case, so the only difference between them is the route the rows
// travelled -- which is what makes this a fixture pin rather than a comparison
// of a thing with itself.
func TestDBSyncStatusOverTheOwnerRoutePrintsTheDirectOpenBytes(t *testing.T) {
	for _, tc := range dbSyncStatusCases {
		t.Run(tc.name, func(t *testing.T) {
			serveSyncStatus(t, tc)

			stdout, _, err := captureOutput(t, func() error { return cmdDBSyncStatus(nil) })
			if err != nil {
				t.Fatalf("cmdDBSyncStatus over the owner route: %v", err)
			}
			if string(stdout) != tc.wantLine {
				t.Errorf("owner-route line = %q, want the golden %q", string(stdout), tc.wantLine)
			}
			if strings.Contains(string(stdout), syncStatusSecret) {
				t.Error("the line carries the stored token value")
			}

			jsonOut, _, err := captureOutput(t, func() error { return cmdDBSyncStatus([]string{"--json"}) })
			if err != nil {
				t.Fatalf("cmdDBSyncStatus --json over the owner route: %v", err)
			}
			if string(jsonOut) != tc.wantDoc {
				t.Errorf("owner-route document = %q, want the golden %q", string(jsonOut), tc.wantDoc)
			}

			// The route is the only variable left: the daemon's own rows, read
			// through the handle the verb actually used.
			if got := dbSyncStatusLine(readSyncStatusDoc(t, dialledLocal(t))); got != tc.wantLine {
				t.Errorf("line from the dialled handle = %q, want %q", got, tc.wantLine)
			}

			direct := directSyncStatus(t, tc)
			if got := dbSyncStatusLine(readSyncStatusDoc(t, direct)); got != tc.wantLine {
				t.Errorf("line from the direct handle = %q, want the golden %q", got, tc.wantLine)
			}
		})
	}
}

// TestDBSyncStatusTokensMatchTheDirectOpenRoute pins the statusline mapping
// rather than the CLI's: off, on, behind and err, each with and without a
// backlog, must come back as the same token whether the markers were read off a
// dialled local handle or a directly opened one. The route moves handles, not
// rows, so a token that differs here is a token the cockpit and the CLI would
// disagree about on the same machine at the same moment.
func TestDBSyncStatusTokensMatchTheDirectOpenRoute(t *testing.T) {
	for _, tc := range dbSyncStatusCases {
		t.Run(tc.name, func(t *testing.T) {
			serveSyncStatus(t, tc)

			gotToken, err := relevosync.StatusToken(dialledLocal(t))
			if err != nil {
				t.Fatalf("StatusToken over the owner route: %v", err)
			}
			if gotToken != tc.wantToken {
				t.Errorf("token over the owner route = %q, want the golden %q", gotToken, tc.wantToken)
			}

			directToken, err := relevosync.StatusToken(directSyncStatus(t, tc))
			if err != nil {
				t.Fatalf("StatusToken over a direct open: %v", err)
			}
			if directToken != tc.wantToken {
				t.Errorf("token over a direct open = %q, want the golden %q", directToken, tc.wantToken)
			}
		})
	}
}

// syncStatusDaemonEnv names the state root the daemon child should open. The
// prefix is deliberately not RELEVO_ or CLAUDE_: TestMain unsets those before
// any test runs, and this variable has to survive into the child.
const syncStatusDaemonEnv = "SYNC_STATUS_DAEMON_ROOT"

// syncStatusDaemonReady is the one line the child prints once it holds the file
// and has bound the socket. The parent waits for it, so the tests never race a
// daemon that is still starting.
const syncStatusDaemonReady = "sync-status-daemon-ready"

// TestDBSyncStatusDaemonChild is the child half of the daemon-running tests. It
// is not a test of anything and asserts nothing: without the environment
// variable it skips, and with it, it is the daemon -- the same open and the same
// serve the daemon itself does -- until the parent kills it.
func TestDBSyncStatusDaemonChild(t *testing.T) {
	root := os.Getenv(syncStatusDaemonEnv)
	if root == "" {
		t.Skip("not the daemon child")
	}
	t.Setenv("XDG_STATE_HOME", root)

	d, err := openDBDirect(machineDBPath())
	if err != nil {
		t.Fatalf("child open: %v", err)
	}
	defer func() { _ = d.Close() }()
	ln, err := openOwnerListener(machineRoot(t))
	if err != nil {
		t.Fatalf("child listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	srv, err := serveOwner(d, ln, nil)
	if err != nil || srv == nil {
		t.Fatalf("child serve: %v, %v", srv, err)
	}
	defer func() { _ = srv.Close() }()

	// The listener is bound, so a dial from the parent will connect; the handshake
	// is answered on accept, which Serve is already doing.
	fmt.Fprintln(os.Stdout, syncStatusDaemonReady)
	for {
		time.Sleep(time.Hour)
	}
}

// startSyncStatusDaemon seeds a fresh state root, then hands the file to a child
// process that is a daemon in every way that matters here: it opens the machine
// database directly -- so it holds the open lock for real -- and serves the
// root's socket. The parent must not be the one holding the file, or there is no
// lock for the daemon to have taken.
func startSyncStatusDaemon(t *testing.T, tc dbSyncStatusCase) {
	t.Helper()
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)

	d, err := openDBDirect(machineDBPath())
	if err != nil {
		t.Fatalf("openDBDirect: %v", err)
	}
	seedSyncStatusLocal(t, d.Local(), tc)
	if err := d.Close(); err != nil {
		t.Fatalf("release the seeded file: %v", err)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^TestDBSyncStatusDaemonChild$")
	cmd.Env = append(os.Environ(), syncStatusDaemonEnv+"="+root)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the daemon child: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	ready := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if line := strings.TrimSpace(sc.Text()); line != "" {
				ready <- line
				return
			}
		}
		close(ready)
	}()
	select {
	case line, ok := <-ready:
		if !ok || line != syncStatusDaemonReady {
			t.Fatalf("the daemon child said %q, want %q", line, syncStatusDaemonReady)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the daemon child never reported ready")
	}
}

// TestDBSyncStatusAnswersWhileTheDaemonRuns is the bug this round exists for: a
// daemon in another process holds relevo.db, and the verb still answers, with the
// bytes the same state prints with no daemon at all. The refusal it used to meet
// is the whole of what this change removes.
func TestDBSyncStatusAnswersWhileTheDaemonRuns(t *testing.T) {
	tc := dbSyncStatusCases[2] // on, no backlog
	startSyncStatusDaemon(t, tc)

	stdout, _, err := captureOutput(t, func() error { return cmdDBSyncStatus(nil) })
	if err != nil {
		t.Fatalf("cmdDBSyncStatus while the daemon runs: %v", err)
	}
	if string(stdout) != tc.wantLine {
		t.Errorf("line = %q, want %q", string(stdout), tc.wantLine)
	}
}

// TestDBSyncWritersStillRefuseWhileTheDaemonRuns pins the other half of the
// split: the direct open the writing verbs need is untouched, so `enable` and
// `push` still refuse with the same conflict and the same `relevo daemon stop`
// hint. enable and push are the two call sites -- the verb that opens the handle
// itself and dbSyncOneShot, which pull share -- so covering them leaves no writer
// unaccounted for. Routing status through the owner must not have softened this
// into a write over a handle a daemon is serving.
func TestDBSyncWritersStillRefuseWhileTheDaemonRuns(t *testing.T) {
	startSyncStatusDaemon(t, dbSyncStatusCases[2])

	for _, verb := range []struct {
		name string
		run  func() error
	}{
		{"enable", func() error { return cmdDBSyncEnable(nil) }},
		{"push", func() error { return cmdDBSyncPush(nil) }},
	} {
		t.Run(verb.name, func(t *testing.T) {
			_, _, err := captureOutput(t, func() error { return verb.run() })
			ce := requireCLIError(t, err, codeConflict, "relevo daemon stop")
			if !strings.Contains(ce.message, "the daemon must not be running") {
				t.Errorf("message = %q, want the daemon named", ce.message)
			}
		})
	}
}

// TestDBSyncStatusRefusesAnOwnerThatServesNoLocalFile pins the half of the
// split that a routed read must not lose. An owner built before the local scope
// existed serves the shared file alone, and the shared file has no sync section
// and no token: answering from it would print a confident "sync off (remote:
// (none), token: absent)" for a machine that is on with a remote and a token
// stored. So the verb must refuse, and refuse with the local file named rather
// than with a line a reader could mistake for an answer.
func TestDBSyncStatusRefusesAnOwnerThatServesNoLocalFile(t *testing.T) {
	tc := dbSyncStatusCases[2] // on, no backlog: the answer a downgrade would lose
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	path := machineDBPath()

	// An upgraded machine first: the split pair, and the local rows on disk.
	pair, err := openDBDirect(path)
	if err != nil {
		t.Fatalf("openDBDirect: %v", err)
	}
	seedSyncStatusLocal(t, pair.Local(), tc)
	if err := pair.Close(); err != nil {
		t.Fatalf("close the split pair: %v", err)
	}

	// Then an owner from before the local scope: the shared file, and no
	// ServeLocal, so its welcome carries HasLocal false.
	shared, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	ln, err := net.Listen("unix", machineSocketPath(t))
	if err != nil {
		t.Fatalf("listen %s: %v", machineSocketPath(t), err)
	}
	srv := db.NewOwner(shared)
	t.Cleanup(func() { _ = srv.Close() })
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = srv.Serve(ln) }()

	dialled, err := dialOwner(t.Context(), machineRoot(t), verbDialBudget)
	if err != nil {
		t.Fatalf("dialOwner: %v", err)
	}
	t.Cleanup(func() { _ = dialled.Close() })
	if dialled.Local() != nil {
		t.Fatal("the owner advertised a machine-local file it does not serve, so this test cannot prove the refusal")
	}

	stdout, _, err := captureOutput(t, func() error { return cmdDBSyncStatus(nil) })
	if err == nil {
		t.Fatal("status must refuse an owner that serves no machine-local file")
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want nothing: a refusal that also answers is the silent downgrade", string(stdout))
	}
	ce := requireCLIError(t, err, codeRefused, "")
	if !strings.Contains(ce.message, "local file") {
		t.Errorf("message = %q, want it to name the machine-local file", ce.message)
	}
}

// TestDBSyncStatusRefusesWithNoOwnerAndNamesTheSocket pins what happens when
// there is nothing to dial. Status has no direct open left to fall back to, so a
// stopped daemon is a refusal -- and it has to name the socket, because that is
// the only thing a reader can act on. It must not name `daemon stop` either: that
// hint belonged to the conflict this route removes, and leaving it would tell a
// user to stop a daemon nothing is asking them to stop.
func TestDBSyncStatusRefusesWithNoOwnerAndNamesTheSocket(t *testing.T) {
	root := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", root)
	if _, err := openDBDirect(machineDBPath()); err != nil {
		t.Fatalf("openDBDirect: %v", err)
	}

	stdout, _, err := captureOutput(t, func() error { return cmdDBSyncStatus(nil) })
	if err == nil {
		t.Fatal("status must refuse when no owner is serving the machine database")
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want nothing", string(stdout))
	}
	ce := requireCLIError(t, err, codeRefused, "")
	if !strings.Contains(ce.message, machineSocketPath(t)) {
		t.Errorf("message = %q, want it to name the socket %s", ce.message, machineSocketPath(t))
	}
	if strings.Contains(ce.message, "daemon stop") {
		t.Errorf("message = %q, want no `daemon stop` hint: status no longer needs the daemon to stop", ce.message)
	}
}
