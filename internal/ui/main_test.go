package ui

import (
	"os"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db/dbtest"
)

func TestMain(m *testing.M) {
	staticCursor = true
	os.Exit(dbtest.Main(m))
}
