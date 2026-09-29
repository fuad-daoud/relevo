package db_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db/dbtest"
)

// TestMain joins the transparency switch: when RELEVO_DBTEST_OWNER is set, every
// database this package opens is reached through an in-process owner, and the
// unset default opens the file directly. dbtest.Main is not used here, because
// this package migrates its own fixtures rather than seeding from the template.
func TestMain(m *testing.M) {
	cleanup, err := dbtest.OwnerMode()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}
