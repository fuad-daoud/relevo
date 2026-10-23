package main

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/doctor"
)

func TestOpenRoundsCheckOKWhenNone(t *testing.T) {
	c := openRoundsCheck(0, 0)
	if c.Severity != doctor.SevOK || c.Name != "open rounds" || c.Detail != "no open round on a finished binding" || c.Fix != "" {
		t.Errorf("openRoundsCheck(0, 0) = %+v", c)
	}
}

func TestOpenRoundsCheckWarnsWithCountsAndSaysReportOnly(t *testing.T) {
	c := openRoundsCheck(3, 2)
	if c.Severity != doctor.SevWarn {
		t.Errorf("severity = %v, want warn", c.Severity)
	}
	want := "3 round(s) still open on 2 finished binding(s); report only, doctor repairs nothing"
	if c.Detail != want {
		t.Errorf("detail = %q, want %q", c.Detail, want)
	}
	if !strings.HasPrefix(c.Fix, "relevo history --q ") {
		t.Errorf("fix = %q, want a history query", c.Fix)
	}
}
