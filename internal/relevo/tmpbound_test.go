package relevo

import (
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestTmpDirLengthBound pins the budget end to end: the longest TMPDIR
// relevo hands a served or local round or check, plus the socket suffix,
// stays under the sun_path limit. The served root below has ownerTmpDir's
// shape (tmp/t/<8>); the local root is tmpdir.TempDir's (<state>/tmp).
func TestTmpDirLengthBound(t *testing.T) {
	t.Parallel()

	servedRoot := filepath.Join("/home", "u", ".local", "state", "relevo-serve", "tmp", "t", strings.Repeat("a", 8))
	localRoot := filepath.Join("/home", "u", ".local", "state", "relevo", "tmp")
	longestName := strings.Repeat("n", store.MaxAgentNameLen)
	zeroID := remote.ClientID("SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32)))
	longestUnit := scopeUnitNameFor(scopeGate, string(zeroID), longestName+"-check", 999999, "")

	cases := map[string]string{
		"served round": filepath.Join(servedRoot, housekeep.RoundTmpRel(longestName, 999999)),
		"served check": filepath.Join(servedRoot, checkTmpRel(longestUnit)),
		"local round":  filepath.Join(localRoot, housekeep.RoundTmpRel(longestName, 999999)),
		"local check":  filepath.Join(localRoot, checkTmpRel(longestUnit)),
	}
	for name, dir := range cases {
		t.Logf("longest %s TMPDIR = %d bytes (+%d suffix)", name, len(dir), housekeep.TmpSocketSuffixLen)
		if housekeep.TmpOverBudget(dir) {
			t.Errorf("longest %s TMPDIR = %q (%d bytes) is over budget", name, dir, len(dir))
		}
		if len(dir)+housekeep.TmpSocketSuffixLen >= 108 {
			t.Errorf("longest %s TMPDIR %q plus suffix reaches the sun_path limit", name, dir)
		}
	}
}
