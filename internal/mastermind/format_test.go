package mastermind

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/jsonshape"
	"github.com/fuad-daoud/relevo/internal/store"
)

// update rewrites the golden file, but only when MasterMindFormat was bumped.
var update = flag.Bool("update", false, "rewrite testdata/record-shape.golden when MasterMindFormat was bumped")

const recordGoldenPath = "testdata/record-shape.golden"

const (
	recordShapeMsg   = "mastermind.Record's JSON shape changed: bump mastermind.MasterMindFormat, then run go test ./internal/mastermind -run TestRecordShapeMatchesFormat -update"
	recordRefusalMsg = "bump mastermind.MasterMindFormat first; an older relevo would erase the new fields"
	recordStaleMsg   = "mastermind.Record's golden format line is stale; run go test ./internal/mastermind -run TestRecordShapeMatchesFormat -update"
)

type recordGolden struct {
	format int
	keys   []string
}

// parseRecordGolden reads "format <N>" on the first line, then one key path
// per line.
func parseRecordGolden(raw []byte) (recordGolden, error) {
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return recordGolden{}, errors.New("golden file is empty")
	}
	rest, ok := strings.CutPrefix(lines[0], "format ")
	if !ok {
		return recordGolden{}, fmt.Errorf("golden first line = %q, want \"format <N>\"", lines[0])
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil {
		return recordGolden{}, fmt.Errorf("golden format line: %w", err)
	}
	return recordGolden{format: n, keys: lines[1:]}, nil
}

func marshalRecordGolden(format int, keys []string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "format %d\n", format)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// goldenDecision is the -update rule: write rewrites the golden, a non-empty
// msg fails the test instead.
func goldenDecision(goldenFormat, codeFormat int, same bool) (write bool, msg string) {
	if same && goldenFormat == codeFormat {
		return false, ""
	}
	if codeFormat > goldenFormat {
		return true, ""
	}
	return false, recordRefusalMsg
}

// checkRecordShape compares keys against the golden and returns the message
// to fail with, or "" when all is well.
func checkRecordShape(g recordGolden, codeFormat int, keys []string, update bool) string {
	same := slices.Equal(g.keys, keys)
	if update {
		write, msg := goldenDecision(g.format, codeFormat, same)
		if msg != "" {
			return msg
		}
		if write {
			if err := os.WriteFile(recordGoldenPath, marshalRecordGolden(codeFormat, keys), 0o644); err != nil {
				return fmt.Sprintf("write %s: %v", recordGoldenPath, err)
			}
			return ""
		}
	}
	if !same {
		return recordShapeMsg
	}
	if g.format != codeFormat {
		if g.format > codeFormat {
			return recordRefusalMsg
		}
		return recordStaleMsg
	}
	return ""
}

// readRecordGolden treats a missing file as format 0 and no keys.
func readRecordGolden(t *testing.T) recordGolden {
	t.Helper()
	raw, err := os.ReadFile(recordGoldenPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return recordGolden{}
		}
		t.Fatalf("read %s: %v", recordGoldenPath, err)
	}
	g, err := parseRecordGolden(raw)
	if err != nil {
		t.Fatalf("%s: %v", recordGoldenPath, err)
	}
	return g
}

// TestRecordShapeMatchesFormat pins Record's JSON shape against the golden:
// a new field is a new key path.
func TestRecordShapeMatchesFormat(t *testing.T) {
	g := readRecordGolden(t)
	keys := jsonshape.Keys(reflect.TypeOf(Record{}))

	if msg := checkRecordShape(g, MasterMindFormat, keys, *update); msg != "" {
		t.Fatal(msg)
	}
	if got := readRecordGolden(t); got.format != MasterMindFormat {
		t.Errorf("golden format = %d, want MasterMindFormat %d", got.format, MasterMindFormat)
	}
}

// TestRecordShapeFailurePath exercises the golden test's failure path: an
// extra field fails with the bump message, and -update without a bump
// refuses.
func TestRecordShapeFailurePath(t *testing.T) {
	type recordWithANewField struct {
		Record
		BrandNew string `json:"brand_new"`
	}
	g := readRecordGolden(t)
	keys := jsonshape.Keys(reflect.TypeOf(recordWithANewField{}))

	if slices.Equal(g.keys, keys) {
		t.Fatal("an extra field must change the key paths")
	}
	if msg := checkRecordShape(g, MasterMindFormat, keys, false); msg != recordShapeMsg {
		t.Errorf("mismatch message = %q, want %q", msg, recordShapeMsg)
	}
	if msg := checkRecordShape(g, MasterMindFormat, keys, true); msg != recordRefusalMsg {
		t.Errorf("-update without a bump = %q, want %q", msg, recordRefusalMsg)
	}
}

func TestStoredFormat(t *testing.T) {
	if got := storedFormat(MasterMindFormat); got != 0 {
		t.Errorf("storedFormat(%d) = %d, want 0: format 1 is absent on disk", MasterMindFormat, got)
	}
	if got := storedFormat(2); got != 2 {
		t.Errorf("storedFormat(2) = %d, want 2", got)
	}
}

// TestRegistryWriteRefusesANewerFormat pins that a record written by a
// newer relevo loads, and writing it back is refused with ErrNewerFormat.
func TestRegistryWriteRefusesANewerFormat(t *testing.T) {
	reg := testRegistry(t)
	rec := record("pl_aaaaaaaaaaaa", "alpha", "claude", "sess-1", 0)
	rec.Format = MasterMindFormat + 1

	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.KV.KVPut(registryKey(rec.ID), raw); err != nil {
		t.Fatal(err)
	}

	got, err := reg.Get(rec.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Format != MasterMindFormat+1 {
		t.Fatalf("loaded Format = %d, want %d", got.Format, MasterMindFormat+1)
	}

	_, err = reg.SetHost(rec.ID, 42, 7)
	var newer *store.ErrNewerFormat
	if !errors.As(err, &newer) {
		t.Fatalf("SetHost on a newer format = %v, want *store.ErrNewerFormat", err)
	}
	if !errors.Is(err, store.ErrNewerFormatSentinel) {
		t.Errorf("errors.Is(%v, ErrNewerFormatSentinel) = false, want true", err)
	}
	if newer.Kind != "mastermind record" || newer.Name != rec.Name || newer.Have != MasterMindFormat+1 || newer.Know != MasterMindFormat {
		t.Errorf("ErrNewerFormat = %+v", newer)
	}
	wantText := `mastermind record "alpha" was written by a newer relevo (format 2; this relevo knows 1): upgrade relevo; a mastermind session reconnects relevo mcp with /mcp`
	if err.Error() != wantText {
		t.Errorf("ErrNewerFormat text = %q, want %q", err.Error(), wantText)
	}

	after, ok, err := reg.KV.KVGet(registryKey(rec.ID))
	if err != nil || !ok {
		t.Fatalf("KVGet after a refused write = (_, %v, %v), want the untouched row", ok, err)
	}
	if !bytes.Equal(raw, after) {
		t.Error("a refused write must leave the record byte-identical")
	}
}

// TestOldTranscriptLocatorKeyLoadsAndIsDroppedOnSave pins the compatibility the
// format reverts buy: a bind.json at format 12 and a mastermind record at
// format 1 may still carry the removed transcript_locator key, and both load
// without error and come back without it after a save.
func TestOldTranscriptLocatorKeyLoadsAndIsDroppedOnSave(t *testing.T) {
	s := store.New(t.TempDir())
	const name = "webshop"
	if err := s.Save(store.Binding{Name: name, CWD: "/repo", State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	rec, ok, err := d.RecordGet(s.Owner(), name)
	if err != nil || !ok {
		t.Fatalf("RecordGet = (ok %v, err %v), want the row", ok, err)
	}
	rec.JSON = string(seedJSONKey(t, rec.JSON, "planner", "transcript_locator", "/tmp/old-planner.jsonl"))
	if _, err := d.RecordPut(rec); err != nil {
		t.Fatalf("RecordPut: %v", err)
	}

	b, err := s.Load(name)
	if err != nil {
		t.Fatalf("Load of a format-12 binding carrying the old key = %v, want nil", err)
	}
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	after, ok, err := d.RecordGet(s.Owner(), name)
	if err != nil || !ok {
		t.Fatalf("RecordGet after Save = (ok %v, err %v), want the row", ok, err)
	}
	if strings.Contains(after.JSON, "transcript_locator") {
		t.Errorf("a saved binding still carries transcript_locator:\n%s", after.JSON)
	}

	reg := testRegistry(t)
	mm := record("pl_aaaaaaaaaaaa", "alpha", "claude", "sess-1", 0)
	mmRaw, err := json.Marshal(mm)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	old := seedJSONKey(t, string(mmRaw), "", "transcript_locator", "/tmp/old-mastermind.jsonl")
	if err := reg.KV.KVPut(registryKey(mm.ID), old); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	if _, err := reg.Get(mm.ID); err != nil {
		t.Fatalf("Get of a format-1 record carrying the old key = %v, want nil", err)
	}
	if err := reg.Touch(mm.ID, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got, ok, err := reg.KV.KVGet(registryKey(mm.ID))
	if err != nil || !ok {
		t.Fatalf("KVGet after Touch = (ok %v, err %v), want the row", ok, err)
	}
	if strings.Contains(string(got), "transcript_locator") {
		t.Errorf("a saved mastermind record still carries transcript_locator:\n%s", got)
	}
}

// seedJSONKey returns raw JSON with key set, nested inside parent when parent is
// non-empty, so a test can seed a key the current types no longer write.
func seedJSONKey(t *testing.T, raw, parent, key string, value any) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	holder := doc
	if parent != "" {
		inner, _ := doc[parent].(map[string]any)
		if inner == nil {
			t.Fatalf("JSON has no %s object:\n%s", parent, raw)
		}
		holder = inner
	}
	holder[key] = value
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return out
}
