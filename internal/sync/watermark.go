package sync

// The `-info` sidecar's revert watermark, and the refusal it can veto a pull
// with forever.
//
// A pull applies the remote's frames and then checkpoints the part of the local
// log the sync engine has taken. That checkpoint is bounded by the watermark the
// engine persisted in `<path>-info` on an earlier attempt. The watermark names a
// frame in the local write-ahead log, so a watermark above the log's highest
// frame names a frame that does not exist and never will: the checkpoint cannot
// be satisfied, and the pull refuses with
//
//	unable to checkpoint synced portion of WAL: result=, watermark=<n>
//
// forever, on every attempt, over a file whose rows are perfectly good. Nothing
// short of editing the sidecar moves it, because the number is exactly what the
// next attempt reads back.
//
// So it is edited here, and only that field. The rest of the sidecar carries the
// client's identity -- `client_unique_id` -- and the generation this file was
// uploaded as, so the file is rewritten in place with the watermark cleared
// rather than removed: deleting it would make the next open a first open, and a
// first open against a remote applies the remote over a live file.

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// infoSuffix is the file the sync engine keeps the client's identity, its
// generation and its revert watermark in.
const infoSuffix = "-info"

// The fields the invalidation clears. The watermark is the one that can veto a
// pull; the salt is the log header the watermark was taken against, which names
// nothing once the frame number it qualified is gone.
const (
	watermarkField = "revert_since_wal_watermark"
	saltField      = "revert_since_wal_salt"
)

// checkpointRefusal is the driver's message for a checkpoint its watermark
// cannot satisfy. The wording is the driver's own, so it is matched as a
// substring: what matters is that the refusal is the checkpoint's, not some
// other failure a pull reports.
const checkpointRefusal = "unable to checkpoint synced portion of WAL"

// The write-ahead log's geometry, which turns the log file's size into the frame
// number it reaches. A log is a fixed header followed by fixed-size frames.
const (
	walSuffix            = "-wal"
	walHeaderBytes       = 32
	walFrameOverhead     = 24
	walHeaderPageSizeAt  = 8
	walHeaderPageSizeLen = 4
	walHeaderReadBytes   = walHeaderPageSizeAt + walHeaderPageSizeLen
)

// refusedWatermark pulls the frame number out of the driver's refusal, so the
// invalidation can check that number against the log rather than assuming every
// checkpoint refusal is a stale watermark.
var refusedWatermark = regexp.MustCompile(`watermark=(\d+)`)

// ErrNoWatermark reports a sidecar that carries no revert watermark at all,
// which is the ordinary case: a file that has never applied a pull.
var ErrNoWatermark = errors.New("sync: this file persists no revert watermark")

// watermarkRefusal is the driver's checkpoint refusal carrying the watermark it
// named, so a caller reaches the number without re-reading the driver's prose.
type watermarkRefusal struct {
	cause     error
	watermark int64
}

func (w *watermarkRefusal) Error() string { return w.cause.Error() }

func (w *watermarkRefusal) Unwrap() error { return w.cause }

// RefusedWatermark is the frame number a driver's checkpoint refusal named, and
// whether err was one. A refusal that names no watermark is not classified: the
// number is what says whether the log could ever reach it, so a refusal without
// one is left for the caller to report.
func RefusedWatermark(err error) (int64, bool) {
	var refused *watermarkRefusal
	if errors.As(err, &refused) {
		return refused.watermark, true
	}
	return 0, false
}

// classifyWatermarkRefusal turns the driver's checkpoint refusal into one this
// package can act on, and returns every other error unchanged.
func classifyWatermarkRefusal(err error) error {
	if err == nil || !isCheckpointRefusal(err) {
		return err
	}
	match := refusedWatermark.FindStringSubmatch(err.Error())
	if match == nil {
		return err
	}
	watermark, cerr := strconv.ParseInt(match[1], 10, 64)
	if cerr != nil {
		return err
	}
	return &watermarkRefusal{cause: err, watermark: watermark}
}

func isCheckpointRefusal(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if strings.Contains(e.Error(), checkpointRefusal) {
			return true
		}
	}
	return false
}

// InfoWatermark is the revert watermark a sidecar persists.
type InfoWatermark struct {
	// Watermark is the frame the pull's checkpoint has to reach.
	Watermark int64
	// Present is whether the sidecar carries the field at all, which is what
	// separates a watermark of zero from no watermark.
	Present bool
}

// ReadInfoWatermark reads the persisted watermark out of a database's `-info`
// sidecar. A sidecar that is absent reports no watermark and no error: a file
// with no sidecar has nothing to invalidate, and the next open builds one. A
// sidecar that is present and unreadable is somebody else's problem and is
// reported rather than treated as absent, because editing past a file this
// cannot parse is how the identity half gets lost.
func ReadInfoWatermark(path string) (InfoWatermark, error) {
	sidecar := path + infoSuffix
	body, err := os.ReadFile(sidecar)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return InfoWatermark{}, nil
		}
		return InfoWatermark{}, fmt.Errorf("sync: read the info sidecar: %w", err)
	}
	info, err := parseInfo(body)
	if err != nil {
		return InfoWatermark{}, err
	}
	raw, ok := info[watermarkField]
	if !ok {
		return InfoWatermark{}, nil
	}
	var watermark int64
	if err := json.Unmarshal(raw, &watermark); err != nil {
		return InfoWatermark{}, fmt.Errorf("sync: parse the %s field: %w", watermarkField, err)
	}
	return InfoWatermark{Watermark: watermark, Present: true}, nil
}

// InvalidateStaleWatermark clears the persisted watermark when the log can never
// reach it, and reports whether it did.
//
// A watermark at or below the log's highest frame is left alone: that one a
// checkpoint can still satisfy, and clearing it would ask the engine to replay
// frames it has already taken. A watermark the log can reach but that no
// checkpoint has satisfied yet is also left alone -- the invalidation is for a
// watermark that is impossible, not for one that is merely pending.
func InvalidateStaleWatermark(path string, maxFrame int64) (bool, error) {
	info, err := ReadInfoWatermark(path)
	if err != nil {
		return false, err
	}
	if !info.Present {
		return false, ErrNoWatermark
	}
	if info.Watermark <= maxFrame {
		return false, nil
	}
	return clearWatermark(path)
}

// clearWatermark rewrites the sidecar with the watermark and its salt removed and
// every other field -- the client's identity above all -- carried over exactly as
// it was.
func clearWatermark(path string) (bool, error) {
	sidecar := path + infoSuffix
	body, err := os.ReadFile(sidecar)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, ErrNoWatermark
		}
		return false, fmt.Errorf("sync: read the info sidecar: %w", err)
	}
	info, err := parseInfo(body)
	if err != nil {
		return false, err
	}
	if _, ok := info[watermarkField]; !ok {
		return false, ErrNoWatermark
	}
	delete(info, watermarkField)
	delete(info, saltField)

	out, err := json.Marshal(info)
	if err != nil {
		return false, fmt.Errorf("sync: encode the info sidecar: %w", err)
	}
	if err := writeFileAtomic(sidecar, out); err != nil {
		return false, err
	}
	return true, nil
}

func parseInfo(body []byte) (map[string]json.RawMessage, error) {
	var info map[string]json.RawMessage
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("sync: parse the info sidecar: %w", err)
	}
	return info, nil
}

// writeFileAtomic replaces a file's contents through a temporary file beside it
// and a rename, so a crash mid-write leaves the previous sidecar whole rather
// than half of one. A sidecar is the engine's own bookkeeping; this rewrites the
// two fields that are stale, not the file.
func writeFileAtomic(path string, body []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("sync: write the info sidecar: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync: write the info sidecar: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync: write the info sidecar: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("sync: write the info sidecar: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("sync: write the info sidecar: %w", err)
	}
	return nil
}

// WALMaxFrame is the highest frame a database's write-ahead log reaches, read
// out of the log's own bytes.
//
// It reads the file rather than asking the engine because the engine's own
// answer is a checkpoint, and a checkpoint moves the backfill floor a reader may
// have pinned its snapshot below -- which is the abort this whole shape exists
// around. The log file's size says the same thing with no write at all.
//
// A database with no log, or a log too short to hold a header, reports no frames:
// nothing has been written to it, so nothing can reach a frame of it.
func WALMaxFrame(path string) (int64, error) {
	body, err := os.ReadFile(path + walSuffix)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("sync: read the write-ahead log: %w", err)
	}
	if len(body) < walHeaderReadBytes {
		return 0, nil
	}
	pageSize := int64(binary.BigEndian.Uint32(body[walHeaderPageSizeAt:walHeaderReadBytes]))
	frame := walFrameOverhead + pageSize
	if pageSize <= 0 || frame <= 0 {
		return 0, nil
	}
	return int64(len(body)-walHeaderBytes) / frame, nil
}
