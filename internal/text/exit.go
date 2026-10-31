package text

// DenialSuffix is the exit entry's permission-blocked suffix, empty when the
// tail held no denial.
func DenialSuffix(line string, isDenial bool) string {
	if !isDenial {
		return ""
	}
	return "; permission-blocked: " + line
}
