package doctor

import "testing"

// TestDoctorReportsEditedHookScripts pins the row: a hook script a person
// edited on disk and the applier kept is named, with the command that puts the
// document's copy back. Nothing else in the system would ever say so, because
// the applier keeps a hand edit silently so a peer's other scripts travel.
func TestDoctorReportsEditedHookScripts(t *testing.T) {
	t.Parallel()

	t.Run("no drift", func(t *testing.T) {
		t.Parallel()
		c := HookFilesCheck(HookFilesCheckInput{Carried: 2})
		if c.Severity != SevOK {
			t.Errorf("severity = %v, want ok for two unedited scripts", c.Severity)
		}
		if c.Fix != "" {
			t.Errorf("fix = %q, want none for an unedited script", c.Fix)
		}
	})

	t.Run("an edited script is named", func(t *testing.T) {
		t.Parallel()
		c := HookFilesCheck(HookFilesCheckInput{
			Carried: 2,
			Edited:  []EditedFile{{Path: "/home/u/.config/hooks/PreToolUse.d/a.sh", Hash: "abc"}},
		})
		if c.Severity != SevWarn {
			t.Errorf("severity = %v, want warn for a kept hand edit", c.Severity)
		}
		if c.Name != "hook-files" {
			t.Errorf("name = %q, want hook-files", c.Name)
		}
		if want := "/home/u/.config/hooks/PreToolUse.d/a.sh"; !contains(c.Detail, want) {
			t.Errorf("detail = %q, want it to name %s", c.Detail, want)
		}
		if c.Fix == "" {
			t.Error("fix is empty, want the command that puts the document's copy back")
		}
	})

	t.Run("nothing carried and nothing edited", func(t *testing.T) {
		t.Parallel()
		c := HookFilesCheck(HookFilesCheckInput{})
		if c.Severity != SevOK || c.Fix != "" {
			t.Errorf("check = %+v, want a quiet row", c)
		}
	})
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
