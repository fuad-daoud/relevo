package mastermind

// MasterMindFormat is the format of the Record JSON this binary writes. Format 1
// is stored as an absent "format" field, so an old record stays unchanged;
// bump this whenever Record's JSON shape changes, so an older relevo refuses
// to overwrite fields it does not know.
const MasterMindFormat = 1

// storedFormat is the number written on disk for format n: format 1 is
// omitted (written as 0); every other format is written as itself.
func storedFormat(n int) int {
	if n == 1 {
		return 0
	}
	return n
}
