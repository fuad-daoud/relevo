package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

func rowNamed(rows []doctor.Check, name string) (doctor.Check, bool) {
	for _, c := range rows {
		if c.Name == name {
			return c, true
		}
	}
	return doctor.Check{}, false
}

func TestDoctorIncludesTmpAndCacheRows(t *testing.T) {
	t.Parallel()

	tmp, cache := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "rounds"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "rounds", "a"), []byte("xyz"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(context.Context, string, ...string) ([]byte, error) { return []byte(cache + "\n"), nil }
	rows := diskChecks(context.Background(), diskInputs{TmpRoot: tmp, Run: run})
	if c, ok := rowNamed(rows, "tmp root"); !ok || !strings.Contains(c.Detail, "rounds") {
		t.Errorf("tmp root row = %+v, %v", c, ok)
	}
	if c, ok := rowNamed(rows, "go cache"); !ok || !strings.Contains(c.Detail, cache) {
		t.Errorf("go cache row = %+v, %v", c, ok)
	}
	if _, ok := rowNamed(rows, "go trimpath"); ok {
		t.Error("no working directory: want no trimpath row")
	}
}

func TestDoctorMissingGoIsInfoRow(t *testing.T) {
	t.Parallel()

	run := func(context.Context, string, ...string) ([]byte, error) {
		return nil, fmt.Errorf("%w", exec.ErrNotFound)
	}
	rows := diskChecks(context.Background(), diskInputs{Run: run})
	c, ok := rowNamed(rows, "go cache")
	if !ok || c.Severity != doctor.SevInfo {
		t.Errorf("go cache row = %+v, %v; want info", c, ok)
	}
}

func TestDoctorTrimpathRowForGoTree(t *testing.T) {
	t.Parallel()

	run := func(context.Context, string, ...string) ([]byte, error) { return nil, exec.ErrNotFound }
	goTree := t.TempDir()
	if err := os.WriteFile(filepath.Join(goTree, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ setting, want string }{{"", "on"}, {"on", "on"}, {"off", "off"}} {
		rows := diskChecks(context.Background(), diskInputs{WorkDir: goTree, Run: run, Settings: relevo.RepoSettings{Trimpath: tc.setting}})
		c, ok := rowNamed(rows, "go trimpath")
		if !ok || !strings.Contains(c.Detail, "trimpath "+tc.want) {
			t.Errorf("setting %q: row = %+v, %v; want %s", tc.setting, c, ok, tc.want)
		}
	}
	rows := diskChecks(context.Background(), diskInputs{WorkDir: t.TempDir(), Run: run})
	if _, ok := rowNamed(rows, "go trimpath"); ok {
		t.Error("a non-Go tree must have no trimpath row")
	}
}
