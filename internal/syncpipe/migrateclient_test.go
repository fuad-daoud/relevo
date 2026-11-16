package syncpipe

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// The migrate verb's own coverage: what the hello carries for it, what one
// call answers with, and what its refusals become.

// TestHelloRequestCarriesMigrateFlag pins that the flag travels on the hello
// alone: a migrate config's line carries "migrate":true, and a normal
// config's line carries no migrate key at all, since the field is omitempty.
func TestHelloRequestCarriesMigrateFlag(t *testing.T) {
	t.Parallel()
	req := helloRequest(handshake())
	if req.Migrate {
		t.Error("helloRequest over a normal config sets Migrate, want it false")
	}
	line, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal hello: %v", err)
	}
	if strings.Contains(string(line), "migrate") {
		t.Errorf("hello line %s carries migrate, want it omitted on a normal hello", line)
	}

	migrating := handshake()
	migrating.Migrate = true
	mreq := helloRequest(migrating)
	if !mreq.Migrate {
		t.Error("helloRequest over a migrate config leaves Migrate false, want it set")
	}
	mline, err := json.Marshal(mreq)
	if err != nil {
		t.Fatalf("marshal migrate hello: %v", err)
	}
	if !strings.Contains(string(mline), `"migrate":true`) {
		t.Errorf("migrate hello line %s misses \"migrate\":true", mline)
	}
}

// TestPipeClientHellosMigrateFlag pins the flag over the real pipe: the worker
// a normal Start opens reports no migrate hello, while a migrate config's
// worker reports one, and both workers still serve.
func TestPipeClientHellosMigrateFlag(t *testing.T) {
	t.Parallel()
	report := func(t *testing.T, cfg Config) string {
		t.Helper()
		var errBuf lockedBuffer
		cfg.Stderr = &errBuf
		c, err := startFakeWorker(t, modeEchoHello, cfg)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		var got string
		deadline := time.Now().Add(5 * time.Second)
		for {
			got = errBuf.String()
			if strings.Contains(got, "tables=") || !time.Now().Before(deadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		// A migration pipe refuses every verb but the migrate one, so the
		// serving proof calls the verb the hello opened the pipe for.
		if cfg.Migrate {
			if _, err := c.Migrate(); err != nil {
				t.Fatalf("Migrate over the worker the hello opened: %v", err)
			}
		} else if _, err := c.Stats(); err != nil {
			t.Fatalf("Stats over the worker the hello opened: %v", err)
		}
		return got
	}

	if got := report(t, handshake()); strings.Contains(got, "migrate") {
		t.Errorf("worker opened with %q, want no migrate hello on a normal Start", got)
	}
	migrating := handshake()
	migrating.Migrate = true
	if got := report(t, migrating); !strings.Contains(got, " migrate") {
		t.Errorf("worker opened with %q, want a migrate hello on a migrate Start", got)
	}
}

// wantMigratedResult is what the well-behaved fake worker's migration converts
// to: every field carried across, with the wire's map read back as a slice in
// origin order.
func wantMigratedResult() synclog.MigrateResult {
	return synclog.MigrateResult{
		AlreadyMigrated: true,
		Origins: []synclog.MigrateEntry{
			{Origin: "origin-a", MigrateOrigin: synclog.MigrateOrigin{
				Rows: 5, Objects: 2, Seq: 6, Floor: 1, LegacyUpto: 6, Label: "mine",
			}},
			{Origin: "origin-b", MigrateOrigin: synclog.MigrateOrigin{
				Rows: 7, Objects: 3, Seq: 9, Floor: 4, LegacyUpto: 9, Label: "the laptop",
			}},
		},
		Rows:            12,
		ObjectsCreated:  4,
		ObjectsVerified: 1,
		RowsSwept:       2,
		SharedRows:      12,
		R2Put:           100,
		R2Get:           200,
		TursoSent:       300,
		TursoReceived:   400,
	}
}

// TestPipeClientMigrateReturnsTheConvertedResult pins one migrate call over the
// pipe: the verb's answer converts field by field, with the origins ordered.
// The fake sets every field, so a converter that dropped one fails here rather
// than answering a zero no test names.
func TestPipeClientMigrateReturnsTheConvertedResult(t *testing.T) {
	t.Parallel()
	// The worker refuses the verb on any other pipe, so the call drives a
	// migrate hello.
	migrating := handshake()
	migrating.Migrate = true
	c, err := startFakeWorker(t, modeNormal, migrating)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, err := c.Migrate()
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if want := wantMigratedResult(); !reflect.DeepEqual(got, want) {
		t.Errorf("Migrate = %+v, want %+v", got, want)
	}
}

// TestPipeClientMigrateIncompleteIsTheIncompleteSentinel pins that a migration
// that found late rows answers as the incomplete sentinel under ErrRefused, so
// the caller runs the verb again rather than latching on a refused remote.
func TestPipeClientMigrateIncompleteIsTheIncompleteSentinel(t *testing.T) {
	t.Parallel()
	migrating := handshake()
	migrating.Migrate = true
	c, err := startFakeWorker(t, modeRefuseMigrateIncomplete, migrating)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, err = c.Migrate()
	if !errors.Is(err, relevosync.ErrMigrateIncomplete) {
		t.Errorf("Migrate = %v, want errors.Is(err, ErrMigrateIncomplete)", err)
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("Migrate = %v, want errors.Is(err, ErrRefused)", err)
	}
	if errors.Is(err, relevosync.ErrRemoteRefused) {
		t.Errorf("Migrate = %v, must not read as ErrRemoteRefused", err)
	}
}

// TestPipeClientMigrateRefusesAnEmptyAnswer pins that a migrate reply with no
// result is an error, not an empty migration: the worker always answers with
// what one call moved and built, so a missing field did not do what was asked.
func TestPipeClientMigrateRefusesAnEmptyAnswer(t *testing.T) {
	t.Parallel()
	migrating := handshake()
	migrating.Migrate = true
	c, err := startFakeWorker(t, modeNoKeyAnswer, migrating)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, err = c.Migrate()
	if err == nil {
		t.Fatal("Migrate over an empty answer = nil, want an error")
	}
	if !strings.Contains(err.Error(), "no result") {
		t.Errorf("Migrate = %v, want the missing result named", err)
	}
}

// TestRefusalErrorMapsMigrateIncompleteToTheIncompleteSentinel pins the class
// mapping without a pipe: an incomplete refusal carries ErrRefused and
// ErrMigrateIncomplete, not the remote-refused sentinel a latch would act on.
func TestRefusalErrorMapsMigrateIncompleteToTheIncompleteSentinel(t *testing.T) {
	t.Parallel()
	err := refusalError(syncworker.Response{
		OK:    false,
		Error: "rows arrived while migrating",
		Code:  syncworker.CodeMigrateIncomplete,
	})
	if !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
	if !errors.Is(err, relevosync.ErrMigrateIncomplete) {
		t.Errorf("err = %v, want ErrMigrateIncomplete", err)
	}
	if errors.Is(err, relevosync.ErrRemoteRefused) {
		t.Errorf("err = %v, must not read as ErrRemoteRefused", err)
	}
}
