package db

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// variedRunnerStream is a value shaped like a sealed runner stream: a long run
// of ordinary words in no particular order. It has to be varied rather than one
// repeated line, because a value that compresses to a fixed pattern is stored
// identically at every compression level -- both levels would then produce the
// same frame and this test would pass without the level having changed.
func variedRunnerStream() []byte {
	words := []string{
		"runner", "builder", "round", "report", "tool", "read", "file",
		"line", "error", "test", "make", "check",
	}
	r := rand.New(rand.NewSource(1))
	var b bytes.Buffer
	for i := 0; i < 4000; i++ {
		b.WriteString(words[r.Intn(len(words))])
		b.WriteByte(' ')
	}
	return b.Bytes()
}

// TestCodecOldFramesStillDecode pins that the shared encoder's compression
// level is a write-time choice only. Every frame already in the database was
// written at the level the encoder used before, and the decoder has to read all
// of them: a decoder that only understood the current level would refuse the
// whole history on the first read after an upgrade.
func TestCodecOldFramesStillDecode(t *testing.T) {
	// The default level is what the shared encoder used before it was pinned:
	// a fresh writer with no level option writes exactly those bytes.
	oldEncoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	payload := variedRunnerStream()
	oldFrame := oldEncoder.EncodeAll(payload, nil)
	if err := oldEncoder.Close(); err != nil {
		t.Fatalf("closing the old encoder: %v", err)
	}

	// The frame is a real one, not an empty buffer that would decode to
	// nothing and let this pass for the wrong reason.
	if len(oldFrame) == 0 || len(oldFrame) >= len(payload) {
		t.Fatalf("the old-level frame is %d bytes for a %d byte value", len(oldFrame), len(payload))
	}

	got, err := decodeColumn(oldFrame, codecZstd)
	if err != nil {
		t.Fatalf("decoding an old-level frame: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("decoded %d bytes, want the original %d", len(got), len(payload))
	}

	// The current encoder is pinned above the default, so a value it stores is
	// a different frame from the old one -- otherwise this test would be
	// asserting nothing about the level having changed.
	newStored, codec := encodeColumn(payload)
	if codec != codecZstd {
		t.Fatalf("codec = %d, want %d", codec, codecZstd)
	}
	if bytes.Equal(newStored, oldFrame) {
		t.Error("the pinned encoder produced the default level's frame")
	}
	roundTripped, err := decodeColumn(newStored, codec)
	if err != nil {
		t.Fatalf("decoding a current frame: %v", err)
	}
	if !bytes.Equal(roundTripped, payload) {
		t.Error("the current frame did not round trip")
	}
}
