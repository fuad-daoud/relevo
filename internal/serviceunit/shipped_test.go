package serviceunit

import (
	"strings"
	"testing"
)

const historicalServiceSHA = "04f37d54b904a3bd09601d72b2a9edee6ca83ad9cd6c9822bb2c8051457659cf"

func TestShippedUnitBeforeMatchesHistoricalBlob(t *testing.T) {
	if !ShippedUnitBefore(ServiceName, historicalServiceSHA) {
		t.Errorf("ShippedUnitBefore(%s, historical) = false, want true", ServiceName)
	}
	if ShippedUnitBefore(PlistName, historicalServiceSHA) {
		t.Error("ShippedUnitBefore(plist, historical service sha) = true, want false")
	}
}

func TestShippedUnitBeforeIndexesCurrentSources(t *testing.T) {
	if !ShippedUnitBefore(ServiceName, SHA256Hex(ShippedService())) {
		t.Error("the shipped service is missing from the index")
	}
	if !ShippedUnitBefore(PlistName, SHA256Hex(ShippedPlistTemplate())) {
		t.Error("the shipped plist template is missing from the index")
	}
}

func TestShippedUnitBeforeRejectsUnknownAndEmpty(t *testing.T) {
	unknown := strings.Repeat("ab", 32)
	if ShippedUnitBefore(ServiceName, unknown) {
		t.Error("an unknown sha reported as shipped")
	}
	if ShippedUnitBefore(ServiceName, "") || ShippedUnitBefore("", historicalServiceSHA) {
		t.Error("an empty argument reported as shipped")
	}
	if ShippedUnitBefore("no-such-unit", historicalServiceSHA) {
		t.Error("an unknown unit name reported as shipped")
	}
}

func TestIsLowerHex64(t *testing.T) {
	for in, want := range map[string]bool{
		strings.Repeat("a", 64): true,
		strings.Repeat("A", 64): false,
		strings.Repeat("a", 63): false,
		strings.Repeat("g", 64): false,
	} {
		if got := isLowerHex64(in); got != want {
			t.Errorf("isLowerHex64(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestShippedUnitsAreEmbedded(t *testing.T) {
	if !strings.Contains(string(ShippedService()), "ExecStart=") {
		t.Error("the shipped service has no ExecStart")
	}
	tpl := string(ShippedPlistTemplate())
	if !strings.Contains(tpl, "@BIN@") || !strings.Contains(tpl, "@HOME@") {
		t.Error("the plist template lost its placeholders")
	}
}
