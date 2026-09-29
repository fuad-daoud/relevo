package bugreport

import (
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
)

// fixedNow is the clock every fixture is built with, so no golden depends on
// when it ran.
var fixedNow = time.Date(2026, 9, 30, 0, 43, 12, 0, time.UTC)

// The markers a fixture hides in the fields a default bundle must never carry:
// a transcript path, a payload body and a hook's output. A test fails when one
// of them reaches a rendering.
const (
	transcriptMarker = "TRANSCRIPT-MARKER: the builder's whole stream"
	payloadMarker    = "PAYLOAD-MARKER: the prompt as it was sent"
	hookOutputMarker = "HOOK-OUTPUT-MARKER: what the hook printed"
	tailMarker       = "Tail-MARKER: the last lines of the log"
)

// The secrets a fixture seeds into the fields a bundle does carry, one of every
// shape the pass knows.
const (
	// Each token-shaped value is split at its scanner-visible prefix: a
	// contiguous token-shaped literal trips repository secret scanning, which
	// reads the blobs, though the value exists only to be redacted.
	githubToken  = "ghp_" + "16C7e42F292c6912E7710c838347Ae178B4a"
	anthropicKey = "sk-ant-" + "api03-AbCdEfGhIjKlMnOpQrStUvWx"
	awsKey       = "AKIAIOSFODNN7EXAMPLE"
	slackToken   = "xox" + "b-123456789012-abcdefghijklmnop"
	jwtToken     = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxIn0.dozjgNryP4J3jVmNHl0w5N"
	pemBlock     = "-----BEGIN PRIVATE KEY-----\nMIIEowIBAAKCAQEAx7\n-----END PRIVATE KEY-----"
	bearerValue  = "s3cr3t-bearer-value"
)

// fixtureSources is the default section list wired to fixture data. The command
// wires the same list to a live runtime; this one keeps the projections and the
// golden testable without a machine.
func fixtureSources() []Source {
	rep, logs := fixtureLogs()
	runs, gates, info := fixtureSide()
	return []Source{
		{Name: SectionEnvironment, Build: func() (Section, error) {
			return EnvironmentSection(EnvFacts{
				Version:      "0.4.2",
				Distribution: "release",
				GoVersion:    "go1.27.0",
				GOOS:         "linux",
				GOARCH:       "amd64",
				StateRoot:    "/home/fuad/.local/state/relevo",
			}), nil
		}},
		{Name: SectionLastError, Build: func() (Section, error) {
			return LastErrorSection(fixtureLastError(), true), nil
		}},
		{Name: SectionDoctor, Build: func() (Section, error) {
			return DoctorSection(fixtureDoctor()), nil
		}},
		{Name: SectionStatus, Build: func() (Section, error) { return StatusSection(rep), nil }},
		{Name: SectionRounds, Build: func() (Section, error) { return RoundsSection(logs, "", 0), nil }},
		{Name: SectionHooks, Build: func() (Section, error) { return HooksSection(runs), nil }},
		{Name: SectionGates, Build: func() (Section, error) { return GatesSection(gates.gates, gates.ledger), nil }},
		{Name: SectionDaemon, Build: func() (Section, error) { return DaemonSection(true, info, true), nil }},
	}
}

// fixtureBundle is the default bundle: every source collected, the pass applied
// and the title the failure rule picks.
func fixtureBundle() Bundle {
	le := fixtureLastError()
	return Redact(Collect(Title("0.4.2", le, true), "0.4.2", fixedNow, fixtureSources()), fixtureRedactor())
}

// fixtureRedactor is the pass the fixtures run through: one home, one user and
// one host, all long enough to replace.
func fixtureRedactor() Redactor {
	return Redactor{Home: "/home/fuad", User: "fuad", Host: "contabo-01"}
}

// fixtureLastError is the failure the bundle carries: an internal error from a
// command whose argv names this machine's home.
func fixtureLastError() LastError {
	return LastError{
		Time:    fixedNow.Add(-90 * time.Second),
		Version: "0.4.2",
		Verb:    "send",
		Argv:    []string{"relevo", "send", "--name", "alpha", "--round", "3"},
		Code:    "internal",
		Message: "write /home/fuad/.local/state/relevo/alpha/003-runner.jsonl: boom on contabo-01",
		Next:    "relevo bugreport",
	}
}

// fixtureDoctor is a doctor run with one failure and one clean row.
func fixtureDoctor() DoctorDoc {
	return DoctorDoc{
		UsableBuilder: true,
		Failures:      1,
		Warnings:      1,
		Checks: []DoctorCheck{
			{Group: "claude", Name: "binary", Severity: "warn", Detail: "not on PATH", Fix: "install claude"},
			{Name: "daemon", Severity: "fail", Detail: "not running", Fix: "relevo daemon", ProbeFailed: false},
			{Name: "release", Severity: "ok", Detail: "0.4.2 is current"},
		},
	}
}

// fixtureLogs is the two bindings the fixture reports on, and their logs.
func fixtureLogs() (view.Report, []BindingLog) {
	rep := view.Report{Bindings: []view.BindingStatus{
		{
			Name:             "alpha",
			Role:             "builder",
			BuilderCandidate: "claude:sonnet",
			State:            "running",
			PlanRound:        3,
			Round:            3,
			Shape:            "",
			Pending:          &view.PendingInfo{Round: 2, Kind: store.KindReport},
			BuilderStatus:    "working",
			Headless:         &view.HeadlessInfo{Tail: []string{tailMarker}},
		},
		{
			Name:             "beta",
			Role:             "builder",
			BuilderCandidate: "claude:haiku",
			State:            "done",
			PlanRound:        1,
			Round:            1,
			Shape:            store.ShapeReader,
			Server:           "box",
			BuilderStatus:    "idle",
		},
	}}

	logs := []BindingLog{
		{Name: "alpha", Entries: []store.LogEntry{
			{
				Seq: 1, TS: fixedNow.Add(-30 * time.Minute), Round: 3, Direction: store.DirToBuilder,
				Kind: store.KindPrompt, Confirmed: true, Route: "deliverer",
				Path:    "/home/fuad/.local/state/relevo/alpha/003-runner.jsonl",
				Note:    "round 3 sent with " + githubToken,
				Payload: payloadMarker,
			},
			{
				Seq: 2, TS: fixedNow.Add(-10 * time.Minute), Round: 3, Direction: store.DirToMasterMind,
				Kind: store.KindReport, Confirmed: true, Late: true, Tier: "plan",
				Outcome: "done", HaltedAt: "", ChangedPaths: []string{"a.go"},
				Usage: &usage.Usage{Tokens: usage.Tokens{In: 1200, CacheRead: 100, Out: 30}},
			},
			{
				Seq: 3, TS: fixedNow.Add(-9 * time.Minute), Round: 3, Direction: store.DirToMasterMind,
				Kind: store.KindDiff, Route: "pull", Payload: transcriptMarker,
				Note: "diff recorded for round 3",
			},
		}},
		{Name: "beta", Entries: []store.LogEntry{
			{Seq: 1, TS: fixedNow.Add(-48 * time.Hour), Round: 1, Direction: store.DirToBuilder,
				Kind: store.KindPrompt, Confirmed: true, Route: "channel"},
		}},
	}

	return rep, logs
}

// fixtureSide is the rest of the machine: the hook runs, the gates and the
// daemon's own record.
func fixtureSide() ([]hooks.HookRun, fixtureGates, store.DaemonInfo) {
	runs := []hooks.HookRun{
		{At: fixedNow.Add(-5 * time.Minute), Event: "state_changed", Argv: []string{"/home/fuad/.local/bin/notify", "--state", "running"}},
		{At: fixedNow.Add(-4 * time.Minute), Event: "round_started",
			Argv: []string{"/usr/local/bin/hook"}, ExitCode: 1, Output: hookOutputMarker,
			Error: "boom: " + anthropicKey + " and Bearer " + bearerValue},
	}

	ledger := fixtureGates{
		gates: []availability.Gate{{
			Token: "claude:sonnet", Name: "sonnet", Kind: availability.RateLimited,
			Since: fixedNow.Add(-20 * time.Minute), Until: fixedNow.Add(40 * time.Minute),
			Note: "usage limit: " + slackToken, Source: "relevo", Binding: "alpha",
		}},
		ledger: availability.Ledger{Entries: []availability.Entry{{
			Kind: availability.SpawnFailed, Subject: "claude:haiku", At: fixedNow.Add(-2 * time.Hour),
			Note: pemBlock + " and " + awsKey + " and " + jwtToken, Source: "relevo", Binding: "beta",
		}}},
	}

	info := store.DaemonInfo{
		Version:   "0.4.2",
		PID:       4242,
		StartedAt: fixedNow.Add(-3 * time.Hour),
		Exe:       "/home/fuad/.local/bin/relevo",
	}
	return runs, ledger, info
}

// fixtureGates is one ledger and the live gates read from it.
type fixtureGates struct {
	gates  []availability.Gate
	ledger availability.Ledger
}
