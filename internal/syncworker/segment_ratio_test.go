package syncworker

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"
)

// The fixture's HMAC key is fixed rather than the machine's secret: the
// fixture only needs MACs that behave like real ones -- 32 bytes no other
// field of the entry can predict -- and a fixed key keeps the fixture, and
// with it the measured ratio, reproducible.
const fixtureMACKey = "relevo segment-ratio fixture key"

// fixturePools is what a busy day reuses across rows: a round names one of a
// few bindings, an event names one of a few chains, a commit hash repeats.
// Pooling them makes the fixture repeat tokens the way a real log does,
// instead of repeating whole rows, which would inflate the ratio.
type fixturePools struct {
	bindings []string
	chains   []string
	records  []string
	commits  []string
	origins  []string
	features []string
}

func newFixturePools(rng *rand.Rand, base time.Time) fixturePools {
	p := fixturePools{
		bindings: make([]string, 8),
		chains:   make([]string, 5),
		records:  make([]string, 8),
		commits:  make([]string, 4),
		origins:  []string{"laptop", "zen", "contabo"},
		features: []string{
			"measure the segment compression ratio on a realistic fixture",
			"cap one segment at 256 entries and retry a failed fetch whole",
			"move the coverage baseline next to the gates",
			"stop the drain when a batch refuses with a marked error",
			"give the cockpit a per-binding traffic sparkline",
		},
	}
	for i := range p.bindings {
		p.bindings[i] = fixtureULID(rng, base.Add(time.Duration(i)*time.Hour))
		p.records[i] = fixtureULID(rng, base.Add(time.Duration(i)*time.Hour+7*time.Minute))
	}
	for i := range p.chains {
		p.chains[i] = fixtureULID(rng, base.Add(time.Duration(i)*2*time.Hour))
	}
	for i := range p.commits {
		p.commits[i] = fixtureHash(rng)
	}
	return p
}

func pickFrom(rng *rand.Rand, from []string) string {
	return from[rng.IntN(len(from))]
}

// fixtureULID returns an id in the shape a real id carries: 26 characters of
// Crockford base32, a millisecond timestamp then random characters, so the
// ids sort and compress like real ones without importing an ULID package.
func fixtureULID(rng *rand.Rand, at time.Time) string {
	const digits = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	id := make([]byte, 26)
	ms := uint64(at.UnixMilli())
	for i := 9; i >= 0; i-- {
		id[i] = digits[ms&0x1f]
		ms >>= 5
	}
	for i := 10; i < 26; i++ {
		id[i] = digits[rng.IntN(len(digits))]
	}
	return string(id)
}

func fixtureHash(rng *rand.Rand) string {
	var b [20]byte
	for i := range b {
		b[i] = byte(rng.IntN(256))
	}
	return hex.EncodeToString(b[:])
}

// fixtureStamp formats a timestamp the way the schema stores them: RFC3339
// UTC text with millisecond precision.
func fixtureStamp(at time.Time) string {
	return at.Format("2006-01-02T15:04:05.000Z")
}

func fixturePath(rng *rand.Rand, pools fixturePools, name string) string {
	return "/srv/data/relevo/serve/bindings/" + pickFrom(rng, pools.commits) + "/.worktrees/" + name
}

func fixtureRoundBody(rng *rand.Rand, pools fixturePools, at time.Time, id string) json.RawMessage {
	started := at.Add(-time.Duration(rng.IntN(50)+10) * time.Minute)
	closed := started.Add(time.Duration(rng.IntN(12)+2) * time.Minute)
	return json.RawMessage(fmt.Sprintf(`{`+
		`"id":%q,"binding_id":%q,"number":%d,`+
		`"started_at":%q,"closed_at":%q,"outcome":%q,`+
		`"builder_candidate":%q,"builder_harness":"opencode","builder_provider":%q,`+
		`"builder_model":%q,"builder_mode":"headless","tier":%q,"commits":%d,`+
		`"tree":"clean","gate_result":%q,"gate_exit":0,"gate_duration_ms":%d,`+
		`"in_tokens":%d,"cache_tokens":%d,"write_tokens":%d,"out_tokens":%d,`+
		`"cost_usd":%f,"cost_basis":"tok","report_outcome":%q,"switches":0}`,
		id, pickFrom(rng, pools.bindings), rng.IntN(12)+1,
		fixtureStamp(started), fixtureStamp(closed),
		pickFrom(rng, []string{"done", "done", "done", "blocked"}),
		pickFrom(rng, []string{"x-rev", "x-lite", "x-sec"}),
		pickFrom(rng, pools.origins),
		pickFrom(rng, []string{"glm-5.3-flash-free", "claude-sonnet-4-6", "gpt-5.2"}),
		pickFrom(rng, []string{"small", "big"}),
		rng.IntN(4),
		pickFrom(rng, []string{"green", "green", "green", "red"}),
		90000+rng.IntN(600000),
		40000+rng.IntN(400000), 10000+rng.IntN(200000), 500+rng.IntN(8000),
		2000+rng.IntN(40000),
		rng.Float64()*0.8,
		pickFrom(rng, []string{"completed", "completed", "notes"}),
	))
}

func fixtureBindingBody(rng *rand.Rand, pools fixturePools, at time.Time, id string) json.RawMessage {
	name := pickFrom(rng, []string{"r2-b", "r3-a", "r1-c", "z2-a"})
	return json.RawMessage(fmt.Sprintf(`{`+
		`"id":%q,"name":%q,"repo_id":%q,"planner_id":%q,`+
		`"feature":%q,"cwd":%q,"worktree":%q,"branch":%q,"base_commit":%q,`+
		`"tier":%q,"gate":"make check-affected","builder_mode":"headless",`+
		`"server":%q,"created_at":%q,"final_state":%q,"ingest_source":"stream"}`,
		id, name, fixtureULID(rng, at), fixtureULID(rng, at),
		pickFrom(rng, pools.features),
		"/srv/data/relevo/"+name, fixturePath(rng, pools, name),
		"relevo/"+name, pickFrom(rng, pools.commits),
		pickFrom(rng, []string{"small", "big"}),
		pickFrom(rng, pools.origins),
		fixtureStamp(at.Add(-time.Duration(rng.IntN(72)+1)*time.Hour)),
		pickFrom(rng, []string{"DONE", "RUNNING", "NEEDS YOU"}),
	))
}

func fixtureBindingEventBody(rng *rand.Rand, pools fixturePools, at time.Time, record string, seq int) json.RawMessage {
	inner := fmt.Sprintf(`{"note":"round %d closed with gate %q","path":"%s"}`,
		rng.IntN(12)+1, pickFrom(rng, []string{"green", "red"}), fixturePath(rng, pools, "r2-b"))
	return json.RawMessage(fmt.Sprintf(`{`+
		`"record_id":%q,"seq":%d,"ts":%q,"round":%d,"direction":%q,`+
		`"kind":%q,"confirmed":1,"delivered_at":%q,"route":%q,"entry_json":%q}`,
		record, seq, fixtureStamp(at), rng.IntN(12)+1,
		pickFrom(rng, []string{"in", "out"}),
		pickFrom(rng, []string{"round_opened", "round_closed", "sent", "delivered"}),
		fixtureStamp(at.Add(time.Duration(rng.IntN(9)+1)*time.Second)),
		pickFrom(rng, []string{"mastermind", "runner", "human"}),
		inner,
	))
}

func fixtureChainEventBody(rng *rand.Rand, pools fixturePools, at time.Time, chain string, seq int) json.RawMessage {
	event := fmt.Sprintf(`{"kind":%q,"outcome":%q,"gate":"green"}`,
		pickFrom(rng, []string{"builder_closed", "reviewer_opened", "gate_ran"}),
		pickFrom(rng, []string{"done", "completed", "notes"}))
	action := fmt.Sprintf(`{"kind":"send","member":%q,"seed":%q}`,
		pickFrom(rng, []string{"r2-b", "r3-a", "r1-c"}),
		pickFrom(rng, []string{"builder", "reviewer", "planner"}))
	return json.RawMessage(fmt.Sprintf(`{`+
		`"chain_id":%q,"seq":%d,"ts":%q,"phase":%q,"step":%q,"member":%q,`+
		`"round":%d,"event":%q,"action":%q,"reason":""}`,
		chain, seq, fixtureStamp(at), pickFrom(rng, []string{"build", "review", "plan"}),
		fmt.Sprint(rng.IntN(5)+1), pickFrom(rng, []string{"r2-b", "r3-a", "r1-c"}),
		rng.IntN(12)+1, event, action,
	))
}

func fixtureDocBody(rng *rand.Rand, at time.Time) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"stamp":"%d","policy":{`+
		`"candidates":["x-rev","x-lite","x-sec"],"gate":"make check-affected",`+
		`"tiers":{"small":{"model":"glm-5.3-flash-free","provider":"opencode"},`+
		`"big":{"model":"claude-sonnet-4-6","provider":"anthropic"}},`+
		`"budget":{"per_round_usd":%.2f,"per_day_usd":%.2f},"updated_at":%q}}`,
		rng.IntN(60)+1,
		0.10+rng.Float64()*0.8, 2.0+rng.Float64()*6.0,
		fixtureStamp(at),
	))
}

// newRealisticEntries builds count contiguous entries for origin as a busy day
// writes them: mostly round and binding rows, some doc:config, timestamps
// stepping forward a few seconds each, an after-dependency on a minority, and
// a MAC over the entry's own bytes. A seeded PCG drives every random choice,
// so the fixture -- and the ratio measured on it -- reproduces byte for byte.
func newRealisticEntries(origin string, startSeq, count int) []Entry {
	rng := rand.New(rand.NewPCG(1653, 256))
	base := time.Date(2026, 11, 13, 8, 3, 11, 0, time.UTC)
	pools := newFixturePools(rng, base)
	entries := make([]Entry, count)
	for i := range entries {
		at := base.Add(time.Duration(i)*time.Duration(3+rng.IntN(7))*time.Second +
			time.Duration(rng.IntN(1000))*time.Millisecond)
		e := Entry{
			Origin:        origin,
			Seq:           startSeq + i,
			Batch:         startSeq + (i/6)*6,
			SchemaVersion: 1,
			At:            at,
			Op:            "upsert",
		}
		switch pick := rng.IntN(100); {
		case pick < 40:
			id := fixtureULID(rng, at)
			e.Tbl, e.PK, e.Body = "round", fmt.Sprintf(`[%q]`, id), fixtureRoundBody(rng, pools, at, id)
		case pick < 65:
			id := fixtureULID(rng, at)
			e.Tbl, e.PK, e.Body = "binding", fmt.Sprintf(`[%q]`, id), fixtureBindingBody(rng, pools, at, id)
		case pick < 75:
			record, seq := pickFrom(rng, pools.records), rng.IntN(400)+1
			e.Tbl, e.PK, e.Body = "binding_event",
				fmt.Sprintf(`[%q,%d]`, record, seq),
				fixtureBindingEventBody(rng, pools, at, record, seq)
		case pick < 87:
			chain, seq := pickFrom(rng, pools.chains), rng.IntN(300)+1
			e.Tbl, e.PK, e.Body = "chain_event",
				fmt.Sprintf(`[%q,%d]`, chain, seq),
				fixtureChainEventBody(rng, pools, at, chain, seq)
		default:
			e.Tbl, e.PK, e.Body = "doc:config",
				pickFrom(rng, []string{`["policy"]`, `["actors"]`, `["gates"]`}),
				fixtureDocBody(rng, at)
		}
		if rng.IntN(29) == 5 {
			// A real day also deletes shared rows: a chain member leaves, an
			// aged history row is pruned. A delete names the row and carries
			// no body.
			e.Op, e.Body = "delete", nil
		}
		if i%9 == 4 {
			e.AfterOrigin = pickFrom(rng, pools.origins)
			e.AfterSeq = startSeq + i - (11 + rng.IntN(40))
		}
		// The MAC covers the entry's own bytes, so it is incompressible the
		// way a real MAC is: no field of the row predicts it.
		line, _ := json.Marshal(e)
		mac := hmac.New(sha256.New, []byte(fixtureMACKey))
		mac.Write(line)
		e.MAC = mac.Sum(nil)
		entries[i] = e
	}
	return entries
}

// segmentRawLength recomputes the uncompressed payload length the way
// encodeSegment sizes it: the header line, then each marshalled entry line
// with its newline.
func segmentRawLength(hdr SegmentHeader, entries []Entry) int {
	line, _ := json.Marshal(hdr)
	total := len(line) + 1
	for i := range entries {
		line, _ := json.Marshal(entries[i])
		total += len(line) + 1
	}
	return total
}

// TestSegmentRatioOnRealisticEntries measures the compression ratio of a
// full segment of 256 realistic entries. The assertion is deliberately a
// loose floor, not the design's 5x: it catches a broken or disabled
// compressor, while the measured figure is reported, never asserted to a
// target the design assumed.
func TestSegmentRatioOnRealisticEntries(t *testing.T) {
	origin := "contabo"
	entries := newRealisticEntries(origin, 1, SegmentMaxEntries)
	data, hdr, err := EncodeSegment(origin, entries)
	if err != nil {
		t.Fatalf("EncodeSegment: %v", err)
	}
	raw := segmentRawLength(hdr, entries)
	packed := len(data)
	ratio := float64(raw) / float64(packed)
	t.Logf("level 9: raw=%d packed=%d ratio=%.1f", raw, packed, ratio)

	// With the MACs dropped: 32 random bytes per entry are 44 base64
	// characters a row's other fields cannot get under, so this ratio says
	// how much of the remaining gap to the design's 5x the MACs explain.
	zeroed := make([]Entry, len(entries))
	copy(zeroed, entries)
	for i := range zeroed {
		zeroed[i].MAC = nil
	}
	zData, zHdr, err := EncodeSegment(origin, zeroed)
	if err != nil {
		t.Fatalf("EncodeSegment without MACs: %v", err)
	}
	zRaw := segmentRawLength(zHdr, zeroed)
	zPacked := len(zData)
	t.Logf("level 9, MACs zeroed: raw=%d packed=%d ratio=%.1f",
		zRaw, zPacked, float64(zRaw)/float64(zPacked))

	if ratio < 2 {
		t.Errorf("ratio = %.1f (raw %d, packed %d), under the broken-compressor floor of 2",
			ratio, raw, packed)
	}
}
