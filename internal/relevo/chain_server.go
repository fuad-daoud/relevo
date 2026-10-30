package relevo

import (
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// missingChainFeatures is the features a server must advertise to run a whole
// chain for a client, in the order the refusal names them: it drives the chain
// itself, and it runs reader members. Both are absent from a pre-chain server.
func missingChainFeatures(who remote.WhoAmI) []string {
	var missing []string
	if !hasFeature(who, remote.FeatureChain) {
		missing = append(missing, remote.FeatureChain)
	}
	if !hasFeature(who, remote.FeatureReaders) {
		missing = append(missing, remote.FeatureReaders)
	}
	return missing
}

// hasFeature reports whether the server advertises token.
func hasFeature(who remote.WhoAmI, token string) bool {
	for _, f := range who.Features {
		if f == token {
			return true
		}
	}
	return false
}

// chainFeatureList renders the missing features for the refusal's parenthetical:
// one is a singular "feature", two or more a plural list.
func chainFeatureList(missing []string) string {
	quoted := make([]string, len(missing))
	for i, f := range missing {
		quoted[i] = strconv.Quote(f)
	}
	if len(quoted) == 1 {
		return quoted[0] + " feature"
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1] + " features"
}
