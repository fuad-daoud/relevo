package remote

import (
	"strconv"
	"strings"
)

// MissingChainFeatures names the features a server must advertise to run a
// whole chain for a client, in the order the refusal names them: it drives
// the chain itself, and it runs reader members.
func MissingChainFeatures(who WhoAmI) []string {
	var missing []string
	if !HasFeature(who, FeatureChain) {
		missing = append(missing, FeatureChain)
	}
	if !HasFeature(who, FeatureReaders) {
		missing = append(missing, FeatureReaders)
	}
	return missing
}

func HasFeature(who WhoAmI, token string) bool {
	for _, f := range who.Features {
		if f == token {
			return true
		}
	}
	return false
}

func ChainFeatureList(missing []string) string {
	quoted := make([]string, len(missing))
	for i, f := range missing {
		quoted[i] = strconv.Quote(f)
	}
	if len(quoted) == 1 {
		return quoted[0] + " feature"
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1] + " features"
}
