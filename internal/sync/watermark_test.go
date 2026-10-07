package sync

// The stale revert watermark: a fixture whose persisted watermark sits above the
// log's highest frame, which no checkpoint can ever satisfy, and the shape of
// the driver's refusal when it refuses.

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeInfo writes a sidecar carrying exactly the fields a driver writes for a
// member that has applied no pull: the identity, the version and a watermark,
// plus the salt that goes with it.
func writeInfo(t *testing.T, path string, info map[string]any) {
	t.Helper()
	body, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("encode the info sidecar: %v", err)
	}
	if err := os.WriteFile(path+infoSuffix, body, 0o600); err != nil {
		t.Fatalf("write the info sidecar: %v", err)
	}
}

// staleInfo is a sidecar whose watermark names a frame past the end of a log of
// frames frames: the shape an aborted pull leaves behind.
func staleInfo(watermark, frames int64) map[string]any {
	return map[string]any{
		"version":                    "v1",
		"client_unique_id":           "turso-sync-go-2f0a1c3e-6b0a-4f0d-9a2e-1b2c3d4e5f60",
		"revert_since_wal_watermark": watermark,
		"revert_since_wal_salt":      []any{1, 2, 3},
		"last_pull_unix_time":        nil,
		"last_push_unix_time":        nil,
		"saved_configuration":        map[string]any{"remote_url": "http://127.0.0.1:1/relevo"},
	}
}

// writeWAL writes a log of frames frames for a database of the given page size,
// which is the geometry WALMaxFrame reads.
func writeWAL(t *testing.T, path string, pageSize int, frames int) {
	t.Helper()
	head := make([]byte, 32)
	binary.BigEndian.PutUint32(head[0:4], 0x377f0682)
	binary.BigEndian.PutUint32(head[4:8], 3007000)
	binary.BigEndian.PutUint32(head[8:12], uint32(pageSize))
	body := append(head, make([]byte, frames*(24+pageSize))...)
	if err := os.WriteFile(path+walSuffix, body, 0o600); err != nil {
		t.Fatalf("write the write-ahead log: %v", err)
	}
}

// TestWALMaxFrameReadsTheLogsOwnSize pins the measure the invalidation compares
// a watermark against: the frame the log reaches, read out of the log's bytes
// with no call to the engine. A checkpoint would answer the same question by
// writing, and a checkpoint is the operation that moves the backfill floor a
// reader may be pinned below.
func TestWALMaxFrameReadsTheLogsOwnSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frames.db")
	writeWAL(t, path, 4096, 216)
	got, err := WALMaxFrame(path)
	if err != nil {
		t.Fatalf("WALMaxFrame: %v", err)
	}
	if got != 216 {
		t.Errorf("WALMaxFrame = %d, want 216", got)
	}

	// A file with no log reaches no frame at all, which is the same answer a log
	// with nothing in it gives.
	bare := filepath.Join(t.TempDir(), "bare.db")
	if got, err := WALMaxFrame(bare); err != nil || got != 0 {
		t.Errorf("WALMaxFrame on a database with no log = (%d, %v), want (0, nil)", got, err)
	}
}

// TestAStaleWatermarkIsInvalidatedAndAReachableOneIsNot is the decision: a
// watermark above the log's highest frame can never be satisfied by any
// checkpoint and is cleared, and one the log can still reach is left alone
// because a checkpoint can satisfy it.
func TestAStaleWatermarkIsInvalidatedAndAReachableOneIsNot(t *testing.T) {
	t.Run("above the log", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "stale.db")
		writeWAL(t, path, 4096, 216)
		writeInfo(t, path, staleInfo(452, 216))

		cleared, err := InvalidateStaleWatermark(path, 216)
		if err != nil {
			t.Fatalf("InvalidateStaleWatermark: %v", err)
		}
		if !cleared {
			t.Fatal("a watermark of 452 against a log of 216 frames was not invalidated, so the pull would refuse forever")
		}

		info, err := ReadInfoWatermark(path)
		if err != nil {
			t.Fatalf("ReadInfoWatermark: %v", err)
		}
		if info.Present {
			t.Errorf("the sidecar still carries a watermark: %d", info.Watermark)
		}
	})

	t.Run("at or below the log", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "reachable.db")
		writeWAL(t, path, 4096, 216)
		writeInfo(t, path, staleInfo(216, 216))

		cleared, err := InvalidateStaleWatermark(path, 216)
		if err != nil {
			t.Fatalf("InvalidateStaleWatermark: %v", err)
		}
		if cleared {
			t.Error("a watermark the log can reach was invalidated, which would ask the engine to replay frames it already took")
		}
	})
}

// TestTheInvalidationLeavesTheClientsIdentity is the half of the sidecar that
// must survive. `client_unique_id` and the generation are what make the next
// open a continuation rather than a first open, and a first open against a
// remote applies the remote over a live file.
func TestTheInvalidationLeavesTheClientsIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.db")
	writeWAL(t, path, 4096, 216)
	writeInfo(t, path, staleInfo(452, 216))

	before, err := ReadInfoWatermark(path)
	if err != nil {
		t.Fatalf("ReadInfoWatermark: %v", err)
	}
	if !before.Present || before.Watermark != 452 {
		t.Fatalf("the fixture persisted watermark %d (present %v), want 452", before.Watermark, before.Present)
	}

	if _, err := InvalidateStaleWatermark(path, 216); err != nil {
		t.Fatalf("InvalidateStaleWatermark: %v", err)
	}

	body, err := os.ReadFile(path + infoSuffix)
	if err != nil {
		t.Fatalf("the invalidation removed the sidecar: %v", err)
	}
	var info map[string]any
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatalf("parse the rewritten sidecar: %v", err)
	}
	if _, ok := info["client_unique_id"]; !ok {
		t.Error("the rewritten sidecar lost client_unique_id, so the next open would be a first open")
	}
	if _, ok := info["version"]; !ok {
		t.Error("the rewritten sidecar lost its version")
	}
	if _, ok := info["saved_configuration"]; !ok {
		t.Error("the rewritten sidecar lost the remote it was opened against")
	}
	if _, ok := info["revert_since_wal_salt"]; ok {
		t.Error("the salt outlived the watermark it qualified")
	}
	if strings.Contains(string(body), "revert_since_wal_watermark") {
		t.Errorf("the watermark is still in the sidecar: %s", body)
	}
}

// TestASecondInvalidationHasNothingLeftToDo pins idempotence: once the stale
// watermark is gone, the next attempt reads no watermark and says so rather
// than editing the sidecar again. A second attempt is the one a reader re-runs
// by hand, so it has to be the cheap answer.
func TestASecondInvalidationHasNothingLeftToDo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twice.db")
	writeWAL(t, path, 4096, 216)
	writeInfo(t, path, staleInfo(452, 216))

	first, err := InvalidateStaleWatermark(path, 216)
	if err != nil {
		t.Fatalf("first invalidation: %v", err)
	}
	if !first {
		t.Fatal("the first invalidation did nothing")
	}
	after, err := os.ReadFile(path + infoSuffix)
	if err != nil {
		t.Fatalf("read the sidecar: %v", err)
	}

	second, err := InvalidateStaleWatermark(path, 216)
	if !errors.Is(err, ErrNoWatermark) {
		t.Errorf("the second invalidation returned (%v, %v), want ErrNoWatermark", second, err)
	}
	if second {
		t.Error("the second invalidation reported work it had no watermark to do")
	}
	again, err := os.ReadFile(path + infoSuffix)
	if err != nil {
		t.Fatalf("read the sidecar again: %v", err)
	}
	if string(again) != string(after) {
		t.Errorf("the second attempt rewrote the sidecar:\nfirst:  %s\nsecond: %s", after, again)
	}
}

// TestOnlyTheCheckpointsRefusalIsClassified pins what is undone. A pull reports
// many failures and only one of them is a watermark no checkpoint can satisfy;
// classifying any of the others would clear a watermark that is doing its job.
func TestOnlyTheCheckpointsRefusalIsClassified(t *testing.T) {
	refused := classifyWatermarkRefusal(errors.New(
		"sync engine operation failed: unable to checkpoint synced portion of WAL: result=, watermark=452"))
	if _, ok := RefusedWatermark(refused); !ok {
		t.Fatalf("the driver's checkpoint refusal was not classified: %v", refused)
	}

	for _, other := range []error{
		nil,
		errors.New("connection lost"),
		errors.New("unable to checkpoint synced portion of WAL: result=1"), // no watermark named
	} {
		if _, ok := RefusedWatermark(classifyWatermarkRefusal(other)); ok {
			t.Errorf("classifyWatermarkRefusal(%v) produced an undoable watermark", other)
		}
	}
}

// TestTheRefusedWatermarkIsTheNumberTheDriverNamed pins that the number comes
// out of the driver's own message rather than being parsed by hand at the call
// site: a refusal naming 452 has to yield 452.
func TestTheRefusedWatermarkIsTheNumberTheDriverNamed(t *testing.T) {
	err := classifyWatermarkRefusal(errors.New(
		"unable to checkpoint synced portion of WAL: result=1, watermark=452, main_wal_salt=7"))
	got, ok := RefusedWatermark(err)
	if !ok {
		t.Fatalf("the refusal was not classified: %v", err)
	}
	if got != 452 {
		t.Errorf("RefusedWatermark = %d, want 452", got)
	}
}
