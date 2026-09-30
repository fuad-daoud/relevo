package availability

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/candidate"
)

// ErrUnknownProvider is returned when a clear names a provider that no
// configured candidate uses and the ledger does not gate.
var ErrUnknownProvider = errors.New("unknown provider")

// Who cleared a gate, as recorded in the history event's Source.
const (
	ClearedByMasterMind = "planner" // relevo gate --clear, local or forwarded over POST /v1/available; the value is state already written
	ClearedByServer     = "server"  // relevo serve available on the server host
)

// ResolveClearSubject decides what `relevo gate --clear <subject>` clears, and
// refuses a subject relevo knows nothing about. Pure: the caller passes the
// configured set (possibly nil) and the already-pruned ledger.
//
//	subject is a group@account account gate key:
//	  provider := the group half
//	  known if group is in set.Providers(),
//	     or if l has a RateLimited entry whose group half is group
//	subject is a configured candidate name:
//	  provider := that candidate's provider
//	subject parses as a candidate token (candidate.ParseRef succeeds):
//	  provider := ref.Provider
//	  known if set != nil and set.Lookup(ref) succeeds,
//	     or if l has a RateLimited entry whose Subject == provider
//	  otherwise: return set.Lookup's error unchanged (it wraps
//	     candidate.ErrUnknownCandidate); when set is nil, return
//	     fmt.Errorf("candidate %q not found (no candidates configured): %w",
//	     subject, candidate.ErrUnknownCandidate)
//	otherwise (a bare provider):
//	  provider := subject
//	  known if provider is in set.Providers(),
//	     or if l has a RateLimited entry whose Subject == provider
//	  otherwise: return an error wrapping ErrUnknownProvider
func ResolveClearSubject(set *candidate.Set, l Ledger, subject string) (provider string, err error) {
	// An account gate key names its group; the account half is the caller's to
	// clear. Recognised first because a candidate name and a candidate token
	// never contain "@".
	if group, accountName, ok := account.ParseGateKey(subject); ok && accountName != "" {
		if gatesProvider(l, group) || slices.Contains(set.Providers(), group) {
			return group, nil
		}
		return "", unknownProviderError{msg: unknownProviderMessage(set, group)}
	}

	// A name is a candidate, and clears that candidate's provider. A provider
	// name is never also a candidate name (candidate names must not equal a
	// configured provider), so this cannot shadow the bare-provider branch
	// below.
	if candidate.IsName(subject) {
		if c, rerr := set.Resolve(subject); rerr == nil {
			return c.Ref().Provider, nil
		}
	}

	if ref, perr := candidate.ParseRef(subject); perr == nil {
		provider = ref.Provider
		if set != nil {
			if _, lerr := set.Lookup(ref); lerr == nil {
				return provider, nil
			} else if !gatesProvider(l, provider) {
				// The token is not configured and nothing is gating its
				// provider: a typo, refused with Lookup's own words.
				return "", lerr
			}
			return provider, nil
		}
		// A nil set has no Lookup to call -- it would dereference the set and
		// panic -- so the no-candidates JSON that a server with no
		// candidates.json reads as is rendered here instead.
		if !gatesProvider(l, provider) {
			return "", fmt.Errorf("candidate %q not found (no candidates configured): %w", subject, candidate.ErrUnknownCandidate)
		}
		return provider, nil
	}

	provider = subject
	if gatesProvider(l, provider) || slices.Contains(set.Providers(), provider) {
		return provider, nil
	}
	return "", unknownProviderError{msg: unknownProviderMessage(set, subject)}
}

// clearSubjects names every ledger subject a clear of provider must remove: the
// bare group and, when no account is named, every group@account key the ledger
// holds. An account clear removes just that key. The bare subject is always
// included so an old-format ledger, which has no account keys, clears exactly
// as it always did.
func clearSubjects(l Ledger, provider, accountName string) []string {
	if accountName != "" {
		return []string{account.GateKey(provider, accountName)}
	}
	subjects := []string{provider}
	for _, e := range l.Entries {
		if e.Kind != RateLimited {
			continue
		}
		group, acct, ok := account.ParseGateKey(e.Subject)
		if ok && group == provider && acct != "" {
			subjects = append(subjects, e.Subject)
		}
	}
	return subjects
}

// gatesProvider reports whether l carries a rate-limit gate on provider, bare
// or on one of its accounts. The caller passes an already-pruned ledger, so
// this is exactly the set of entries a clear would remove.
func gatesProvider(l Ledger, provider string) bool {
	for _, e := range l.Entries {
		if e.Kind != RateLimited {
			continue
		}
		group, _, ok := account.ParseGateKey(e.Subject)
		if ok && group == provider {
			return true
		}
	}
	return false
}

// unknownProviderError is ErrUnknownProvider carrying the unknown-provider
// message verbatim. It is a type rather than fmt.Errorf("...: %w",
// ErrUnknownProvider) because %w would append ": unknown provider" to the words
// the refusal is specified to print; Unwrap keeps errors.Is working.
type unknownProviderError struct{ msg string }

func (e unknownProviderError) Error() string { return e.msg }

// Unwrap makes errors.Is(err, ErrUnknownProvider) true.
func (e unknownProviderError) Unwrap() error { return ErrUnknownProvider }

// unknownProviderMessage renders the refusal for a bare subject:
//
//	no configured candidate uses provider "clinepass" (known: anthropic, cline-pass, openai); did you mean "cline-pass"?
//	no configured candidate uses provider "zzz" (known: anthropic, cline-pass, openai)
//
// A nil or empty set reads as "(no candidates configured)", and the suggestion
// tail appears only when suggestProvider names one.
func unknownProviderMessage(set *candidate.Set, subject string) string {
	known := set.Providers()
	where := "(no candidates configured)"
	if len(known) > 0 {
		where = fmt.Sprintf("(known: %s)", strings.Join(known, ", "))
	}

	msg := fmt.Sprintf("no configured candidate uses provider %q %s", subject, where)
	if s := suggestProvider(known, subject); s != "" {
		msg += fmt.Sprintf("; did you mean %q?", s)
	}
	return msg
}

// suggestProvider returns the known provider closest to subject, or "".
//
//	norm(x) := lower-case x with every rune outside [a-z0-9] removed
//	a known p qualifies when norm(p) == norm(subject)          -> distance 0
//	                    or editDistance(lower(p), lower(subject)) <= 2
//	return the qualifier with the smallest distance, where a norm match counts
//	as 0; on a tie, the alphabetically first. None -> "".
func suggestProvider(known []string, subject string) string {
	ns := normProvider(subject)
	lowerSubject := strings.ToLower(subject)

	best := ""
	bestDist := 0
	for _, p := range known {
		d := editDistance(strings.ToLower(p), lowerSubject)
		switch {
		case normProvider(p) == ns:
			d = 0
		case d <= 2:
		default:
			continue
		}
		if best == "" || d < bestDist || (d == bestDist && p < best) {
			best, bestDist = p, d
		}
	}
	return best
}

// normProvider is suggestProvider's norm: lower-case x without every rune
// outside [a-z0-9], so "cline-pass" and "clinepass" are the same word.
func normProvider(x string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(x) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// editDistance is Levenshtein distance over runes. Pure, unexported.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}

	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}

	return prev[len(br)]
}
