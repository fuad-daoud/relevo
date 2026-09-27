package mastermind

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// agyConversation is a valid agy conversation id (the lower-case
// 8-4-4-4-12 hex UUID shape Detect accepts).
const agyConversation = "0f0e0d0c-0b0a-4998-8877-665544332211"

func envFunc(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

func procStartAt(started int64) func(int) (int64, error) {
	return func(int) (int64, error) { return started, nil }
}

func procStartFails() func(int) (int64, error) {
	return func(int) (int64, error) { return 0, errors.New("ps: no such process") }
}

func withEnv(m map[string]string, key, value string) map[string]string {
	out := make(map[string]string, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	out[key] = value
	return out
}

// claudeEnv is a minimal Claude Code environment for host pid and session.
func claudeEnv(pid int, session string) map[string]string {
	return map[string]string{
		"CLAUDECODE":             "1",
		"CLAUDE_PID":             strconv.Itoa(pid),
		"CLAUDE_CODE_SESSION_ID": session,
	}
}

// resolveOrderCases is TestResolveOrder's table. Every id is a literal the
// test also uses to create the matching record, so the table needs no
// runtime state.
var resolveOrderCases = []struct {
	name    string
	in      ResolveInput
	wantID  string
	wantRes Resolution
}{
	{
		name: "flag beats env, host and session",
		in: ResolveInput{
			Flag:      "beta",
			Env:       envFunc(withEnv(claudeEnv(101, "sess-a"), "RELEVO_MASTERMIND", "alpha")),
			PPID:      101,
			ProcStart: procStartAt(1010),
		},
		wantID:  "pl_bbbbbbbbbbbb",
		wantRes: ResolutionFlag,
	},
	{
		name: "flag by id",
		in: ResolveInput{
			Flag:      "pl_aaaaaaaaaaaa",
			Env:       envFunc(map[string]string{}),
			PPID:      999,
			ProcStart: procStartAt(0),
		},
		wantID:  "pl_aaaaaaaaaaaa",
		wantRes: ResolutionFlag,
	},
	{
		// A legacy record's id is a ULID: uppercase, so it can never collide
		// with a (lowercase) name.
		name: "flag by a legacy ULID id",
		in: ResolveInput{
			Flag:      "01M3252956S27X5G5MPVM77PJ7",
			Env:       envFunc(map[string]string{}),
			PPID:      999,
			ProcStart: procStartAt(0),
		},
		wantID:  "01M3252956S27X5G5MPVM77PJ7",
		wantRes: ResolutionFlag,
	},
	{
		name: "env beats host and session",
		in: ResolveInput{
			Env:       envFunc(withEnv(claudeEnv(101, "sess-a"), "RELEVO_MASTERMIND", "beta")),
			PPID:      101,
			ProcStart: procStartAt(1010),
		},
		wantID:  "pl_bbbbbbbbbbbb",
		wantRes: ResolutionEnv,
	},
	{
		name: "env by id",
		in: ResolveInput{
			Env:       envFunc(withEnv(claudeEnv(0, ""), "RELEVO_MASTERMIND", "pl_aaaaaaaaaaaa")),
			PPID:      999,
			ProcStart: procStartAt(0),
		},
		wantID:  "pl_aaaaaaaaaaaa",
		wantRes: ResolutionEnv,
	},
	{
		name: "host beats session",
		in: ResolveInput{
			Env:       envFunc(claudeEnv(101, "sess-b")),
			PPID:      999,
			ProcStart: procStartAt(1010),
		},
		wantID:  "pl_aaaaaaaaaaaa",
		wantRes: ResolutionHost,
	},
	{
		name: "session when the host matches nothing",
		in: ResolveInput{
			Env:       envFunc(claudeEnv(777, "sess-b")),
			PPID:      777,
			ProcStart: procStartAt(7770),
		},
		wantID:  "pl_bbbbbbbbbbbb",
		wantRes: ResolutionSession,
	},
	{
		name: "a host ProcStart error falls through to session",
		in: ResolveInput{
			Env:       envFunc(claudeEnv(101, "sess-b")),
			PPID:      101,
			ProcStart: procStartFails(),
		},
		wantID:  "pl_bbbbbbbbbbbb",
		wantRes: ResolutionSession,
	},
	{
		name: "an agy conversation id resolves through the session step",
		in: ResolveInput{
			Env:       envFunc(map[string]string{"ANTIGRAVITY_CONVERSATION_ID": agyConversation}),
			PPID:      999,
			ProcStart: procStartAt(0),
			Now:       testNow,
		},
		wantID:  "pl_cccccccccccc",
		wantRes: ResolutionSession,
	},
	{
		name: "the parent pid is the host when CLAUDE_PID is unset",
		in: ResolveInput{
			Env:       envFunc(map[string]string{"CLAUDECODE": "1", "CLAUDE_CODE_SESSION_ID": "sess-b"}),
			PPID:      102,
			ProcStart: procStartAt(1020),
		},
		wantID:  "pl_bbbbbbbbbbbb",
		wantRes: ResolutionHost,
	},
}

// TestResolveOrder is the whole precedence rule in one table: flag > env >
// host > session, a host whose ProcStart fails falling through to session,
// and a flag matching nothing being ErrUnknownMasterMind.
func TestResolveOrder(t *testing.T) {
	reg := testRegistry(t)
	mustCreate(t, reg, record("pl_aaaaaaaaaaaa", "alpha", "claude", "sess-a", 101))
	mustCreate(t, reg, record("pl_bbbbbbbbbbbb", "beta", "claude", "sess-b", 102))
	mustCreate(t, reg, record("01M3252956S27X5G5MPVM77PJ7", "legacy", "claude", "sess-c", 103))
	mustCreate(t, reg, record("pl_cccccccccccc", "agy-plane", "agy", agyConversation, 0))

	for _, tc := range resolveOrderCases {
		t.Run(tc.name, func(t *testing.T) {
			rec, res, err := Resolve(reg, tc.in)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if rec.ID != tc.wantID {
				t.Errorf("resolved %s (%s), want %s", rec.ID, rec.Name, tc.wantID)
			}
			if res != tc.wantRes {
				t.Errorf("resolution = %q, want %q", res, tc.wantRes)
			}
		})
	}

	_, _, err := Resolve(reg, ResolveInput{
		Flag:      "nope",
		Env:       envFunc(claudeEnv(101, "sess-a")),
		PPID:      101,
		ProcStart: procStartAt(1010),
	})
	var unknown ErrUnknownMasterMind
	if !errors.As(err, &unknown) || unknown.Ref != "nope" {
		t.Errorf("unknown flag = %v, want ErrUnknownMasterMind{Ref: nope}", err)
	}
	if errors.Is(err, ErrNoMasterMind) {
		t.Errorf("unknown flag reported ErrNoMasterMind; an unknown ref is the other error")
	}

	_, _, err = Resolve(reg, ResolveInput{
		Env:       envFunc(withEnv(claudeEnv(0, ""), "RELEVO_MASTERMIND", "nope")),
		PPID:      999,
		ProcStart: procStartAt(0),
	})
	if !errors.As(err, &unknown) || unknown.Ref != "nope" {
		t.Errorf("unknown env = %v, want ErrUnknownMasterMind{Ref: nope}", err)
	}

	if _, _, err := Resolve(reg, ResolveInput{Env: envFunc(map[string]string{}), PPID: 999}); !errors.Is(err, ErrNoMasterMind) {
		t.Errorf("no detection = %v, want ErrNoMasterMind", err)
	}

	rec, err := reg.Get("pl_bbbbbbbbbbbb")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.SeenAt.Before(testNow) {
		t.Errorf("SeenAt = %v, want it stamped at or after the pinned now %v", rec.SeenAt, testNow)
	}
}

// TestResolveNeverCreates pins "Resolve never registers": an empty registry
// resolves nothing, says so with ErrNoMasterMind, and leaves no row behind.
func TestResolveNeverCreates(t *testing.T) {
	reg := testRegistry(t)

	in := ResolveInput{
		Env:       envFunc(map[string]string{"CLAUDECODE": "1", "CLAUDE_CODE_SESSION_ID": "sess-a"}),
		PPID:      101,
		ProcStart: procStartAt(1010),
		Now:       testNow,
	}
	if _, _, err := Resolve(reg, in); !errors.Is(err, ErrNoMasterMind) {
		t.Fatalf("Resolve on an empty registry = %v, want ErrNoMasterMind", err)
	}

	if _, _, err := Resolve(reg, ResolveInput{Flag: "ghost", Env: in.Env, PPID: in.PPID, Now: testNow}); !errors.Is(err, ErrUnknownMasterMind{Ref: "ghost"}) {
		t.Fatalf("Resolve with an unknown flag = %v, want ErrUnknownMasterMind", err)
	}

	keys, err := reg.KV.KVKeys(mastermindKeyPrefix)
	if err != nil {
		t.Fatalf("KVKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("resolve left %v in the registry; it must create nothing", keys)
	}
}

// TestDetectOnlyClaude pins Detect: only claude is detected, and its host pid
// is CLAUDE_PID when that parses, else the caller's parent.
func TestDetectOnlyClaude(t *testing.T) {
	if _, ok := Detect(envFunc(map[string]string{"OPENCODE_TERMINAL": "1"}), 42); ok {
		t.Error("Detect reported opencode; only claude is detectable")
	}
	if _, ok := Detect(nil, 42); ok {
		t.Error("Detect(nil) reported a harness")
	}

	ident, ok := Detect(envFunc(claudeEnv(4242, "sess-a")), 42)
	if !ok {
		t.Fatal("Detect did not report claude")
	}
	if ident.Kind != "claude" || ident.SessionID != "sess-a" || ident.HostPID != 4242 {
		t.Errorf("Detect = %+v, want claude/sess-a/4242", ident)
	}

	ident, ok = Detect(envFunc(map[string]string{"CLAUDECODE": "1", "CLAUDE_CODE_SESSION_ID": "sess-a"}), 42)
	if !ok || ident.HostPID != 42 {
		t.Errorf("Detect without CLAUDE_PID = %+v (ok %v), want HostPID 42", ident, ok)
	}

	for _, pid := range []string{"", "0", "-3", "not-a-pid"} {
		ident, ok = Detect(envFunc(map[string]string{"CLAUDECODE": "1", "CLAUDE_PID": pid}), 42)
		if !ok || ident.HostPID != 42 {
			t.Errorf("Detect with CLAUDE_PID=%q = %+v (ok %v), want HostPID 42", pid, ident, ok)
		}
	}
}

// TestDetectAgy pins Detect's agy rule: a valid ANTIGRAVITY_CONVERSATION_ID
// is an agy ident with no host pid, anything else is not detected, and
// CLAUDECODE still wins when both are set.
func TestDetectAgy(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want Ident
		ok   bool
	}{
		{
			name: "a valid conversation id is agy with no host pid",
			env:  map[string]string{"ANTIGRAVITY_CONVERSATION_ID": agyConversation},
			want: Ident{Kind: "agy", SessionID: agyConversation, HostPID: 0},
			ok:   true,
		},
		{
			name: "claude wins when both are set",
			env: map[string]string{
				"CLAUDECODE":                  "1",
				"CLAUDE_CODE_SESSION_ID":      "sess-a",
				"ANTIGRAVITY_CONVERSATION_ID": agyConversation,
			},
			want: Ident{Kind: "claude", SessionID: "sess-a", HostPID: 4242},
			ok:   true,
		},
		{
			name: "a malformed id is not detected",
			env:  map[string]string{"ANTIGRAVITY_CONVERSATION_ID": "not-a-uuid"},
		},
		{
			name: "an upper-case UUID is not detected",
			env:  map[string]string{"ANTIGRAVITY_CONVERSATION_ID": strings.ToUpper(agyConversation)},
		},
		{
			name: "a truncated UUID is not detected",
			env:  map[string]string{"ANTIGRAVITY_CONVERSATION_ID": "0f0e0d0c-0b0a-4998-8877-66554433221"},
		},
		{
			name: "an empty id is not detected",
			env:  map[string]string{"ANTIGRAVITY_CONVERSATION_ID": ""},
		},
		{
			name: "no variables at all",
			env:  map[string]string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ident, ok := Detect(envFunc(tc.env), 4242)
			if ok != tc.ok {
				t.Fatalf("Detect ok = %v, want %v", ok, tc.ok)
			}
			if ident != tc.want {
				t.Errorf("Detect = %+v, want %+v", ident, tc.want)
			}
		})
	}

	if _, ok := Detect(nil, 42); ok {
		t.Error("Detect(nil) reported a harness")
	}
}

func TestDetectOpencodeMarker(t *testing.T) {
	ident, ok := Detect(envFunc(map[string]string{"RELEVO_HARNESS": "opencode"}), 42)
	if !ok {
		t.Fatal("Detect did not report opencode")
	}
	if ident.Kind != "opencode" || ident.SessionID != "" || ident.HostPID != 0 {
		t.Errorf("Detect = %+v, want opencode//0", ident)
	}

	ident, ok = Detect(envFunc(map[string]string{
		"RELEVO_HARNESS": "opencode",
		"CLAUDECODE":     "1",
		"CLAUDE_PID":     "100",
	}), 42)
	if !ok || ident.Kind != "claude" {
		t.Errorf("Detect with CLAUDECODE=1 = %+v (ok %v), want claude", ident, ok)
	}

	ident, ok = Detect(envFunc(map[string]string{
		"RELEVO_HARNESS":              "opencode",
		"ANTIGRAVITY_CONVERSATION_ID": agyConversation,
	}), 42)
	if !ok || ident.Kind != "agy" {
		t.Errorf("Detect with agy conversation = %+v (ok %v), want agy", ident, ok)
	}
}

func TestResolveOpencodeSession(t *testing.T) {
	reg := testRegistry(t)
	ocRec := mustCreate(t, reg, record("pl_occccccccccc", "oc-one", "opencode", "ses_resolved", 0))
	opencodeEnv := envFunc(map[string]string{"RELEVO_HARNESS": "opencode"})

	t.Run("finder returns an id with a record -> that record, ResolutionSession", func(t *testing.T) {
		checkResolveOpencodeMatched(t, reg, ocRec)
	})
	t.Run("id without a record -> errors.As ErrUnregisteredSession AND errors.Is ErrNoMasterMind", func(t *testing.T) {
		checkResolveOpencodeUnregistered(t, reg, opencodeEnv)
	})
	t.Run("finder returns ErrNoOpencodeSession -> ErrNoMasterMind", func(t *testing.T) {
		checkResolveOpencodeNoMatch(t, reg, opencodeEnv)
	})
	t.Run("finder returns ambiguous -> that error", func(t *testing.T) {
		checkResolveOpencodeAmbiguous(t, reg, opencodeEnv)
	})
	t.Run("nil finder or CWD \"\" -> ErrNoMasterMind without calling it", func(t *testing.T) {
		checkResolveOpencodeNilOrEmptyCWD(t, reg, opencodeEnv)
	})
	t.Run("RELEVO_MASTERMIND set -> env wins and the finder is never called", func(t *testing.T) {
		checkResolveOpencodeEnvWins(t, reg, ocRec)
	})
}

func checkResolveOpencodeMatched(t *testing.T, reg Registry, ocRec Record) {
	t.Helper()
	rec, res, err := Resolve(reg, ResolveInput{
		Env: envFunc(map[string]string{"RELEVO_HARNESS": "opencode"}),
		CWD: "/repo",
		OpencodeSession: func(cwd string, now time.Time) (string, error) {
			return "ses_resolved", nil
		},
		Now: testNow,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rec.ID != ocRec.ID {
		t.Errorf("rec.ID = %q, want %q", rec.ID, ocRec.ID)
	}
	if res != ResolutionSession {
		t.Errorf("resolution = %q, want %q", res, ResolutionSession)
	}
}

func checkResolveOpencodeUnregistered(t *testing.T, reg Registry, env func(string) string) {
	t.Helper()
	_, _, err := Resolve(reg, ResolveInput{
		Env: env,
		CWD: "/repo",
		OpencodeSession: func(cwd string, now time.Time) (string, error) {
			return "ses_unregistered", nil
		},
		Now: testNow,
	})
	var unreg ErrUnregisteredSession
	if !errors.As(err, &unreg) {
		t.Fatalf("err = %v, want ErrUnregisteredSession", err)
	}
	if unreg.Kind != "opencode" || unreg.SessionID != "ses_unregistered" {
		t.Errorf("unreg = %+v, want opencode/ses_unregistered", unreg)
	}
	if !errors.Is(err, ErrNoMasterMind) {
		t.Errorf("err = %v, want errors.Is ErrNoMasterMind", err)
	}
}

func checkResolveOpencodeNoMatch(t *testing.T, reg Registry, env func(string) string) {
	t.Helper()
	_, _, err := Resolve(reg, ResolveInput{
		Env: env,
		CWD: "/repo",
		OpencodeSession: func(cwd string, now time.Time) (string, error) {
			return "", ErrNoOpencodeSession
		},
		Now: testNow,
	})
	if !errors.Is(err, ErrNoMasterMind) {
		t.Fatalf("err = %v, want ErrNoMasterMind", err)
	}
}

func checkResolveOpencodeAmbiguous(t *testing.T, reg Registry, env func(string) string) {
	t.Helper()
	ambErr := ErrAmbiguousOpencodeSession{Dir: "/repo", Titles: []string{"A", "B"}}
	_, _, err := Resolve(reg, ResolveInput{
		Env: env,
		CWD: "/repo",
		OpencodeSession: func(cwd string, now time.Time) (string, error) {
			return "", ambErr
		},
		Now: testNow,
	})
	if !errors.Is(err, ambErr) {
		t.Fatalf("err = %v, want %v", err, ambErr)
	}
}

func checkResolveOpencodeNilOrEmptyCWD(t *testing.T, reg Registry, env func(string) string) {
	t.Helper()
	called := false
	finder := func(cwd string, now time.Time) (string, error) {
		called = true
		return "ses_resolved", nil
	}

	_, _, err := Resolve(reg, ResolveInput{Env: env, CWD: "/repo", Now: testNow})
	if !errors.Is(err, ErrNoMasterMind) {
		t.Fatalf("nil finder: err = %v, want ErrNoMasterMind", err)
	}

	_, _, err = Resolve(reg, ResolveInput{Env: env, CWD: "", OpencodeSession: finder, Now: testNow})
	if !errors.Is(err, ErrNoMasterMind) {
		t.Fatalf("empty CWD: err = %v, want ErrNoMasterMind", err)
	}
	if called {
		t.Fatal("finder was called when CWD was empty")
	}
}

func checkResolveOpencodeEnvWins(t *testing.T, reg Registry, ocRec Record) {
	t.Helper()
	called := false
	finder := func(cwd string, now time.Time) (string, error) {
		called = true
		return "ses_resolved", nil
	}

	rec, res, err := Resolve(reg, ResolveInput{
		Env: envFunc(map[string]string{
			"RELEVO_HARNESS":    "opencode",
			"RELEVO_MASTERMIND": ocRec.ID,
		}),
		CWD:             "/repo",
		OpencodeSession: finder,
		Now:             testNow,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rec.ID != ocRec.ID {
		t.Errorf("rec.ID = %q, want %q", rec.ID, ocRec.ID)
	}
	if res != ResolutionEnv {
		t.Errorf("res = %q, want %q", res, ResolutionEnv)
	}
	if called {
		t.Fatal("finder was called even though RELEVO_MASTERMIND was set")
	}
}
