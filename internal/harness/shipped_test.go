package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/fuad-daoud/relevo/internal/shipped"
)

func TestBeforeCoversEveryWorkingTreeDefinition(t *testing.T) {
	for _, h := range All() {
		for _, r := range h.Roles {
			doc, err := AgentDoc(r.Name, h.Kind)
			if err != nil {
				t.Fatalf("AgentDoc(%s, %s): %v", r.Name, h.Kind, err)
			}
			base := agentDocBase(r, h.Kind)
			docSum := sha256.Sum256(doc)
			if !shipped.Before(base, hex.EncodeToString(docSum[:])) {
				t.Errorf("shipped.Before(%s, sha of the shipped %s/%s definition) = false, want true",
					base, h.Kind, r.Name)
			}
		}
	}
}
