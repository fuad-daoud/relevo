package serviceunit

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/shipped"
)

func TestUnitBeforeIndexesCurrentSources(t *testing.T) {
	if !shipped.UnitBefore(ServiceName, SHA256Hex(ShippedService())) {
		t.Error("the shipped service is missing from the index")
	}
	if !shipped.UnitBefore(PlistName, SHA256Hex(ShippedPlistTemplate())) {
		t.Error("the shipped plist template is missing from the index")
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
