package syncworker

import "regexp"

// logRowOverhead is what a log row costs beside its body, for the columns the
// body does not carry.
const logRowOverhead = 96

// quotaWord is the error code Turso documents for a plan quota. It is matched
// as a whole word, case-sensitively: it is a code and not a sentence, and
// matching inside a longer word would latch on unrelated text.
var quotaWord = regexp.MustCompile(`\bBLOCKED\b`)

// isQuotaRefusal reports whether err carries Turso's quota code. The driver's
// TURSO_DATABASE_FULL is deliberately not matched: it is SQLite's local
// disk-full code, and latching it would call a full local disk a reached quota.
func isQuotaRefusal(err error) bool {
	return quotaWord.MatchString(err.Error())
}

// markTransferRefusal classes an error from a push or a pull. Those are the
// only calls that reach Turso -- every statement runs on the local replica -- so
// they are the only place a plan quota can refuse, and the only errors the quota
// word is looked for in. An error from reading a row is never searched for it:
// the row's text is the remote's, and a value that spelt the word would latch
// every machine that read it.
func markTransferRefusal(err error) error {
	if isQuotaRefusal(err) {
		return MarkRefusal(CodeQuota, err)
	}
	return markDriverRefusal(err)
}
