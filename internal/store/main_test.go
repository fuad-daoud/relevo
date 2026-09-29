package store

import (
	"os"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db/dbtest"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(m))
}
