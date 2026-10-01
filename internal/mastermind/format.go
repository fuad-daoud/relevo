package mastermind

// MasterMindFormat is the format of the Record JSON this binary writes. Format 1
// is stored as an absent "format" field, so an old record stays unchanged.
//
// Bump this only when an older relevo would drop or misread a field it does not
// know: a new field it would lose on a rewrite, or an existing field whose
// meaning changes. Removing a field needs no bump, because encoding/json ignores
// keys a relevo does not know: an older relevo reading a newer record sees the
// removed field missing, and a key it writes back is ignored by the new one.
const MasterMindFormat = 1

// storedFormat is the number written on disk for format n: format 1 is
// omitted (written as 0); every other format is written as itself.
func storedFormat(n int) int {
	if n == 1 {
		return 0
	}
	return n
}
