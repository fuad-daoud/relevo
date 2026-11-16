package syncworker

// The migration's writer: one origin's format-5 log rows repacked as legacy
// segments, create-only, verified byte for byte against an existing key. Plan
// 8's Migrate is the only caller; nothing here runs until it does.

import (
	"bytes"
	"context"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// originWrite is what migrating one origin moved: the rows read, the objects
// created and the ones verified instead, the sum of every segment object's
// size whether created or verified, the last sequence migrated and the first
// sequence of the last segment -- its key -- and the R2 deltas, which also
// fold into the backend's own counters as each transfer lands.
type originWrite struct {
	rows      int
	created   int
	verified  int
	bytes     int64
	last      int
	lastFirst int
	putBytes  int64
	getBytes  int64
}

// writeLegacySegments migrates one origin's format-5 rows as legacy segments
// of up to SegmentLegacyMaxEntries entries, sparse seqs intact. Chunks always
// start from seq 0, so a re-run re-verifies the objects the last run wrote.
// fromSeq is the origin's log max from logOriginMaxes: it lets the over-range
// refusal run before any put, without another query. Nothing is ever deleted
// or overwritten; an existing key is kept only when it verifies.
func (b *TursoBackend) writeLegacySegments(ctx context.Context, origin string, fromSeq int) (originWrite, error) {
	if err := blobstore.CheckOrigin(origin); err != nil {
		return originWrite{}, MarkRefusal(CodeRemote,
			fmt.Errorf("syncworker: refusing to migrate origin %q: %w", origin, err))
	}
	// Readers skip an origin past the key range, so migrating one would be a
	// silent loss and dropping it irreversible: refuse before any put.
	if fromSeq > maxSegmentSeq {
		return originWrite{}, MarkRefusal(CodeRemote,
			fmt.Errorf("syncworker: refusing to migrate origin %s: seq %d is past the segment key range", origin, fromSeq))
	}
	var out originWrite
	for after := 0; ; {
		planned, err := b.readLogChunk(ctx, origin, after, SegmentLegacyMaxEntries)
		if err != nil {
			return originWrite{}, err
		}
		if len(planned) == 0 {
			break
		}
		for rest := planned; len(rest) > 0; {
			n, err := b.putLegacyObject(ctx, origin, rest, &out)
			if err != nil {
				return originWrite{}, err
			}
			out.rows += n
			out.last = rest[n-1].Seq
			out.lastFirst = rest[0].Seq
			rest = rest[n:]
		}
		after = out.last
	}
	return out, nil
}

// putLegacyObject publishes chunk under the key of its first sequence, and
// reports how many of chunk's entries the object covered. A created key covers
// the whole chunk; an existing one is read back and either verifies whole or
// extends as the tail below.
func (b *TursoBackend) putLegacyObject(ctx context.Context, origin string, chunk []Entry, out *originWrite) (int, error) {
	data, _, err := EncodeLegacySegment(origin, chunk)
	if err != nil {
		return 0, err
	}
	key := blobstore.SegmentKey(origin, chunk[0].Seq)
	created, err := b.blobs.PutIfAbsent(ctx, key, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0, fmt.Errorf("syncworker: put segment %s: %w", key, err)
	}
	if created {
		out.created++
		out.bytes += int64(len(data))
		out.putBytes += int64(len(data))
		b.bytes.R2Put += int64(len(data))
		return len(chunk), nil
	}
	return b.verifyLegacyObject(ctx, origin, key, data, chunk, out)
}

// verifyLegacyObject reads back the object a lost create found. Equal bytes
// verify the whole chunk; anything else is either the shorter run's tail or a
// refusal. The fetch is capped like fetchSegments, so a hostile object cannot
// spend the host's memory before the comparison refuses it.
func (b *TursoBackend) verifyLegacyObject(ctx context.Context, origin, key string, data []byte, planned []Entry, out *originWrite) (int, error) {
	var buf bytes.Buffer
	got, err := b.blobs.Get(ctx, key, &capWriter{w: &buf, left: segmentMaxEncoded})
	if err != nil {
		return 0, blobFetchError(key, err)
	}
	out.getBytes += got
	b.bytes.R2Get += got
	if bytes.Equal(buf.Bytes(), data) {
		out.verified++
		out.bytes += got
		return len(planned), nil
	}
	return b.extendLegacyTail(origin, key, buf.Bytes(), planned, out, got)
}

// extendLegacyTail keeps an existing object that is the planned chunk's
// prefix: a tail from an earlier, shorter run, whose entries the log still
// holds. The remainder of the chunk goes out under a new key, starting after
// the tail's last sequence. Every other difference refuses with the key
// named: the object differs from the log's rows and is left untouched.
func (b *TursoBackend) extendLegacyTail(origin, key string, found []byte, planned []Entry, out *originWrite, got int64) (int, error) {
	header, entries, err := DecodeSegment(key, found)
	if err != nil {
		return 0, legacyDifferRefusal(key, err)
	}
	// The key was built from the planned first and the decoder holds the
	// header to it, so a decoded header already starts at the planned first.
	if !header.Legacy || len(entries) >= len(planned) {
		return 0, legacyDifferRefusal(key, nil)
	}
	prefix, _, err := EncodeLegacySegment(origin, planned[:len(entries)])
	if err != nil {
		return 0, err
	}
	if !bytes.Equal(prefix, found) {
		return 0, legacyDifferRefusal(key, nil)
	}
	out.verified++
	out.bytes += got
	return len(entries), nil
}

// legacyDifferRefusal marks the refusal an object that differs from the log
// earns, naming the key it was kept under. A differing object is never
// deleted or overwritten, so the refusal repeats until a human looks.
func legacyDifferRefusal(key string, err error) error {
	if err != nil {
		return MarkRefusal(CodeRemote,
			fmt.Errorf("syncworker: %s differs from the log's rows and was not touched: %w", key, err))
	}
	return MarkRefusal(CodeRemote,
		fmt.Errorf("syncworker: %s differs from the log's rows and was not touched", key))
}
