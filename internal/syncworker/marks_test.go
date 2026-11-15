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

// TestUpgradeReportsAFailedPush pins that an upgrade whose push did not reach the
// remote is reported. The replica already holds the new table and the new format
// row, so the next open finds a remote that is in neither shape and refuses it
// rather than half-upgrading -- which is why a lost push has to be a failure the
// caller sees.
func TestUpgradeReportsAFailedPush(t *testing.T) {
	path := tempReplica(t)
	execOn(t, path, logWithoutMAC, headTableStatement, createStatements[1],
		"INSERT INTO meta (key, value) VALUES ('"+metaFormatKey+"', '1')")
	driver := &localDriver{path: path, pushErr: errors.New("the remote is not answering")}

	b := NewTursoBackend(driver)
	err := b.Open(Spec{Version: ProtocolVersion, Origin: "origin-a",
		URL: "libsql://remote.example", Token: "the-token"})
	if err == nil {
		t.Fatal("an upgrade whose push failed came back as a success")
	}
	if !contains(err.Error(), "push the upgraded log") {
		t.Errorf("refusal %q does not say the push is what failed", err)
	}
	_ = b.Close()
}
