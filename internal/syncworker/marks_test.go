package syncworker

import (
	"errors"
	"testing"
)

// TestQuotaWordInARowIsNotAQuota pins that the quota word is only looked for in
// errors from a push or a pull. An error a local statement raised can carry a
// row's text, and the remote's rows are not this machine's to trust.
func TestQuotaWordInARowIsNotAQuota(t *testing.T) {
	t.Parallel()
	err := markDriverRefusal(errors.New(`strconv.Atoi: parsing "BLOCKED": invalid syntax`))
	if got := RefusalCodeOf(err); got == CodeQuota {
		t.Fatalf("a local parse error carrying the word was marked %q", got)
	}
}
