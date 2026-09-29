package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/doctor"
)

func TestOwnerCheck(t *testing.T) {
	ok := ownerCheck(db.OwnerStatus{Socket: "/tmp/rvo/o.sock", PID: 42, Version: 1, Conns: 3}, nil)
	if ok.Name != "owner" || ok.Severity != doctor.SevOK {
		t.Fatalf("ok row = %+v, want name owner and severity ok", ok)
	}
	for _, want := range []string{"/tmp/rvo/o.sock", "pid 42", "relevo-owner v1", "3 connections"} {
		if !strings.Contains(ok.Detail, want) {
			t.Errorf("detail %q must contain %q", ok.Detail, want)
		}
	}

	warn := ownerCheck(db.OwnerStatus{Socket: "/tmp/rvo/o.sock"}, errors.New("no such file"))
	if warn.Severity != doctor.SevWarn {
		t.Errorf("a refused probe must warn, got %v", warn.Severity)
	}
	if warn.Fix == "" {
		t.Error("the warn row must carry a fix")
	}
	if !strings.Contains(warn.Detail, "/tmp/rvo/o.sock") {
		t.Errorf("warn detail %q must name the socket", warn.Detail)
	}
}
