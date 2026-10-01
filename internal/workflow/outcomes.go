package workflow

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/reporttail"
)

// ParseOutcomes reads every declared outcome key from the bodies, which the
// caller orders newest first: the final message, then the stream's assistant
// texts. Each key takes the first body that gives a valid value.
//
// The second result is the halt reason, or empty. It names the first failing
// key in sorted order: missing, when no body carries the key, or invalid, when
// a body carries a value the declaration does not admit.
func ParseOutcomes(o Outputs, bodies ...[]byte) (map[string]string, string) {
	values := make(map[string]string)
	for _, key := range o.Outcomes() {
		out := o[key]
		found, first := false, ""
		for _, body := range bodies {
			val, ok := reporttail.BlockValue(body, key)
			if !ok {
				continue
			}
			if !found {
				found, first = true, val
			}
			if outcomeValueValid(out, val) {
				values[key] = val
				break
			}
		}
		if _, ok := values[key]; ok {
			continue
		}
		if !found {
			return nil, key + ": no relevo block carries it"
		}
		return nil, outcomeInvalidReason(key, out, first)
	}
	return values, ""
}

// outcomeValueValid reports whether a body's value satisfies an outcome's
// declaration: a count is a non-negative integer, a one-of is one of its
// values. An artifact is never parsed from a block.
func outcomeValueValid(out Output, val string) bool {
	switch out.Kind {
	case OutputCount:
		n, err := strconv.Atoi(val)
		return err == nil && n >= 0
	case OutputOneOf:
		for _, allowed := range out.Values {
			if val == allowed {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// outcomeInvalidReason names the key and the value that failed it.
func outcomeInvalidReason(key string, out Output, val string) string {
	if out.Kind == OutputCount {
		return fmt.Sprintf("%s: %q is not a count", key, val)
	}
	return fmt.Sprintf("%s: %q is not one of %s", key, val, strings.Join(out.Values, ", "))
}

// MissingArtifact names the first declared artifact, in sorted order, that is
// absent or empty, or returns empty when every one is present and non-empty.
func MissingArtifact(o Outputs, sizes map[string]int64) string {
	for _, name := range o.Artifacts() {
		if sizes[name] <= 0 {
			return name + ": not written or empty"
		}
	}
	return ""
}
