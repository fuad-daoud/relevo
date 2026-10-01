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
	if got := storedFormat(1); got != 0 {
		t.Errorf("storedFormat(1) = %d, want 0: format 1 is absent on disk", got)
	}
	if got := storedFormat(MasterMindFormat); got != MasterMindFormat {
		t.Errorf("storedFormat(%d) = %d, want %d", MasterMindFormat, got, MasterMindFormat)
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
	wantText := `mastermind record "alpha" was written by a newer relevo (format 3; this relevo knows 2): upgrade relevo; a mastermind session reconnects relevo mcp with /mcp`
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
