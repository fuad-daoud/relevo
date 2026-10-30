package shipped

import (
	"strings"
	"testing"
)

const historicalUnitSHA = "04f37d54b904a3bd09601d72b2a9edee6ca83ad9cd6c9822bb2c8051457659cf"

func TestUnitBeforeMatchesHistoricalBlob(t *testing.T) {
	if !UnitBefore("relevo.service", historicalUnitSHA) {
		t.Errorf("UnitBefore(relevo.service, historical) = false, want true")
	}
	if UnitBefore("com.github.fuad-daoud.relevo.plist.in", historicalUnitSHA) {
		t.Error("UnitBefore(plist, historical service sha) = true, want false")
	}
}

func TestUnitBeforeRejectsUnknownAndEmpty(t *testing.T) {
	unknown := strings.Repeat("ab", 32)
	if UnitBefore("relevo.service", unknown) {
		t.Error("an unknown sha reported as shipped")
	}
	if UnitBefore("relevo.service", "") || UnitBefore("", historicalUnitSHA) {
		t.Error("an empty argument reported as shipped")
	}
	if UnitBefore("no-such-unit", historicalUnitSHA) {
		t.Error("an unknown unit name reported as shipped")
	}
}

func TestUnitsIndexLinesAreWellFormed(t *testing.T) {
	lines := strings.Split(strings.TrimRight(unitsSHA256, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("units.sha256 is empty")
	}
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Errorf("line %d = %q, want exactly two fields", i+1, line)
			continue
		}
		if !isLowerHex64(fields[0]) {
			t.Errorf("line %d sha = %q, want 64 lowercase hex digits", i+1, fields[0])
		}
		if strings.ContainsAny(fields[1], "/ \t") || fields[1] == "" {
			t.Errorf("line %d name = %q, want a bare file name", i+1, fields[1])
		}
	}
}
