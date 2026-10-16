package syncpipe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/fuad-daoud/relevo/internal/synclog"
	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// The key verbs over a real pipe: the two calls a machine makes before it can
// sign anything, driven against a child process rather than a function so the
// framing is exercised too.

func TestKeyMetaReadsWhatTheWorkerHolds(t *testing.T) {
	c, err := startFakeWorker(t, modeNormal, handshake())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	key, err := c.KeyMeta(context.Background())
	if err != nil {
		t.Fatalf("KeyMeta: %v", err)
	}
	if !key.Present || key.KDF != "argon2id" {
		t.Errorf("KeyMeta = %+v, want the material the fake log holds", key)
	}
	if !bytes.Equal(key.Salt, []byte("salt")) {
		t.Errorf("KeyMeta salt = %q, want %q: the bytes did not survive the pipe", key.Salt, "salt")
	}
}

// TestKeyInitAnswersWithWhatTheRemoteHolds pins the rule the two racing machines
// depend on: the answer is the remote's material, not the request's. A client
// that echoed its own values back would let a machine that lost the race believe
// it had set the key.
func TestKeyInitAnswersWithWhatTheRemoteHolds(t *testing.T) {
	c, err := startFakeWorker(t, modeNormal, handshake())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The fake log already holds a salt, so this init must be ignored and the
	// held one answered.
	lost := synclog.KeyMeta{
		Salt: []byte("this machine's own salt"), KDF: "argon2id",
		Check: []byte("this machine's own check"), Present: true,
	}
	got, err := c.KeyInit(context.Background(), lost)
	if err != nil {
		t.Fatalf("KeyInit: %v", err)
	}
	if !bytes.Equal(got.Salt, []byte("salt")) {
		t.Errorf("KeyInit answered the salt %q, want the one the log already held", got.Salt)
	}
	if bytes.Equal(got.Check, lost.Check) {
		t.Error("KeyInit echoed the request's check back: the losing machine would believe it set the key")
	}
	if !got.Present {
		t.Error("KeyInit answered with nothing present, want the log's material")
	}
}

// TestKeyMetaRefusedByTheWorker pins that a refusal crosses the pipe as one
// rather than as a zero value read as "the log has no key". Those two mean
// opposite things to a machine deciding whether it is locked.
func TestKeyMetaRefusedByTheWorker(t *testing.T) {
	c, err := startFakeWorker(t, modeRefuseVerbs, handshake())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	key, err := c.KeyMeta(context.Background())
	if err == nil {
		t.Fatalf("KeyMeta = %+v, want a refusal", key)
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("KeyMeta error = %v, want a refusal the breaker leaves alone", err)
	}
}

// TestKeyMetaRefusedWhenTheWorkerAnswersWithNoKey pins the reply the client
// cannot use. The worker always fills the field it is asked about, so an answer
// without one is a worker that did not do what it was asked; reading it as "no
// key" would send the caller down the setup path again against a log that
// already holds one.
func TestKeyMetaRefusedWhenTheWorkerAnswersWithNoKey(t *testing.T) {
	c, err := startFakeWorker(t, modeNoKeyAnswer, handshake())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := c.KeyMeta(context.Background()); err == nil {
		t.Fatal("KeyMeta over a reply with no key in it came back as a success")
	}
}

// serveNoKeyField answers the handshake and then answers every verb with a
// reply carrying no field at all.
//
// The replies are written by hand rather than through Serve because the worker's
// own handlers always fill the field they answer with, so the only way to reach
// a caller is with a pipe that does not.
func serveNoKeyField() int {
	if !answerHandshake(sameID) {
		return 1
	}
	for {
		line, err := readLine(os.Stdin)
		if err != nil {
			return 0
		}
		var req syncworker.Request
		if err := json.Unmarshal(line, &req); err != nil {
			return 1
		}
		out, err := json.Marshal(syncworker.Response{ID: req.ID, OK: true})
		if err != nil {
			return 1
		}
		if _, err := os.Stdout.Write(append(out, '\n')); err != nil {
			return 1
		}
	}
}

// TestKeyRekeyAnswersWithWhatTheRemoteHolds pins the rekey call over the pipe:
// the epoch the caller read, the material and the cuts cross as one request,
// and the answer is what the remote holds afterwards rather than an echo.
func TestKeyRekeyAnswersWithWhatTheRemoteHolds(t *testing.T) {
	c, err := startFakeWorker(t, modeNormal, handshake())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	spec := synclog.KeyRekeySpec{
		From: 1,
		Key: synclog.KeyMeta{
			Salt: []byte("this machine's new salt"), KDF: "argon2id",
			Check: []byte("this machine's new check"), Present: true,
			Epoch: 2, Wrapped: map[int][]byte{1: []byte("the sealed epoch key")},
		},
		Cuts: map[string]synclog.EpochCut{
			"origin-b": {Epoch: 2, Seq: 3, Tag: []byte("the cut's tag")},
		},
	}
	got, err := c.KeyRekey(context.Background(), spec)
	if err != nil {
		t.Fatalf("KeyRekey: %v", err)
	}
	// The fake log already holds material, so the answer is that material --
	// the remote's own, not the request's echo.
	if !bytes.Equal(got.Salt, []byte("salt")) {
		t.Errorf("KeyRekey answered the salt %q, want the one the log holds", got.Salt)
	}
	if got.Epoch != 1 {
		t.Errorf("KeyRekey answered epoch %d, want the log's own 1", got.Epoch)
	}
}

// TestSupervisorDrivesTheKeyVerbs pins that the two verbs run through the
// breaker like every other call: a worker that dies on one is counted, so a
// remote that cannot be asked is backed off rather than retried forever.
func TestSupervisorDrivesTheKeyVerbs(t *testing.T) {
	s := newSupervisor(t, fakeWorkerCfg(modeNormal), breakerOver(newMemKV()))
	key, err := s.KeyMeta(context.Background())
	if err != nil {
		t.Fatalf("Supervisor.KeyMeta: %v", err)
	}
	if !key.Present {
		t.Errorf("Supervisor.KeyMeta = %+v, want the material the fake log holds", key)
	}
	got, err := s.KeyInit(context.Background(), synclog.KeyMeta{
		Salt: []byte("ignored"), KDF: "argon2id", Check: []byte("ignored"),
	})
	if err != nil {
		t.Fatalf("Supervisor.KeyInit: %v", err)
	}
	if !bytes.Equal(got.Salt, []byte("salt")) {
		t.Errorf("Supervisor.KeyInit answered the salt %q, want the log's own", got.Salt)
	}
	rekeyed, err := s.KeyRekey(context.Background(), synclog.KeyRekeySpec{
		From: 1,
		Key: synclog.KeyMeta{
			Salt: []byte("ignored"), KDF: "argon2id", Check: []byte("ignored"),
			Present: true, Epoch: 2, Wrapped: map[int][]byte{1: []byte("sealed")},
		},
	})
	if err != nil {
		t.Fatalf("Supervisor.KeyRekey: %v", err)
	}
	if !bytes.Equal(rekeyed.Salt, []byte("salt")) {
		t.Errorf("Supervisor.KeyRekey answered the salt %q, want the log's own", rekeyed.Salt)
	}
}

// TestKeyCutAnswersTheForwardOnlyVouch pins the cut extension over the pipe:
// the cut crosses as one request and the answer carries no payload, which is
// the worker saying the vouch landed.
func TestKeyCutAnswersTheForwardOnlyVouch(t *testing.T) {
	c, err := startFakeWorker(t, modeNormal, handshake())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := c.PublishEpochCut(context.Background(), synclog.EpochCut{
		Epoch: 2, Seq: 3, Tag: []byte("the cut's tag"),
	}); err != nil {
		t.Fatalf("PublishEpochCut: %v", err)
	}
}

// TestSupervisorPublishesTheCutExtension runs the extension through the
// supervisor, so the breaker counts a worker that dies on it the same way it
// counts any other call.
func TestSupervisorPublishesTheCutExtension(t *testing.T) {
	s := newSupervisor(t, fakeWorkerCfg(modeNormal), breakerOver(newMemKV()))
	if err := s.PublishEpochCut(context.Background(), synclog.EpochCut{
		Epoch: 2, Seq: 3, Tag: []byte("the cut's tag"),
	}); err != nil {
		t.Fatalf("Supervisor.PublishEpochCut: %v", err)
	}
}
