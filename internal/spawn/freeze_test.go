package spawn

import "testing"

// TestIsFrozenUnitError pins the frozen-slice signature: the manager's own
// phrases classify, whatever runner prefix wraps them, and anything else does
// not. A builder that merely muses about something frozen must never read as
// an environmental hold.
func TestIsFrozenUnitError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		text string
		want bool
	}{
		{"manager line", "Cannot start frozen unit relevo.slice", true},
		{"manager line lowercased", "cannot start frozen unit relevo-round-local-webshop-1.scope", true},
		{"operation variant", "Cannot perform operation on frozen unit systemd-udevd.service", true},
		{"frozen slice wording", "Cannot start frozen slice relevo.slice", true},
		{"frozen scope wording", "Cannot start frozen scope relevo-round-local-webshop-1.scope", true},
		{"job result", "Job 9876 for unit 'session-6.scope' failed with 'frozen'", true},
		{"runner wrapper", "Failed to start transient scope unit: Unit relevo-round-local-webshop-1.scope was already loaded: hostfailure", false},
		{"runner wrapper over frozen", "Failed to start transient scope unit: Cannot start frozen unit relevo.slice", true},
		{"scope collision", "Failed to start transient scope unit: Unit relevo-round-local-webshop-1.scope was already loaded", false},
		{"crash", "panic: runtime error: invalid memory address or nil pointer dereference", false},
		{"outage", "API error (attempt 1): UNAVAILABLE (code 503): No capacity available", false},
		{"empty", "", false},
		{"bare frozen musings", "the UI is frozen after resume", false},
		{"freeze verb alone", "freezing the unit before sleep", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsFrozenUnitError(tc.text); got != tc.want {
				t.Errorf("IsFrozenUnitError(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
