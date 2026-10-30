package housekeep

import (
	"strings"
	"testing"
)

// TestShortTmpIDShape pins the derivation's output: 8 lower-case hex
// characters, so every leaf fits the budget whatever the input held.
func TestShortTmpIDShape(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"webshop",
		"relevo-gate-abcdef12-shop-check-3",
		"a binding name with spaces and/slashes",
		"",
	} {
		got := shortTmpID(input)
		if len(got) != tmpShortHexLen {
			t.Errorf("shortTmpID(%q) = %q, want %d characters", input, got, tmpShortHexLen)
		}
		for _, c := range got {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				t.Errorf("shortTmpID(%q) = %q, want lower-case hex only", input, got)
				break
			}
		}
		if !IsShortTmpID(got) {
			t.Errorf("IsShortTmpID(%q) = false, want the derivation's own output recognised", got)
		}
	}
}

// TestShortTmpIDDeterministic pins that the same long identity always maps
// to the same leaf: a restart re-run must find its first run's directory.
func TestShortTmpIDDeterministic(t *testing.T) {
	t.Parallel()

	if got, again := ShortRoundID("webshop", 4), ShortRoundID("webshop", 4); got != again {
		t.Errorf("ShortRoundID not deterministic: %q vs %q", got, again)
	}
	if got, again := ShortCheckID("relevo-gate-local-webshop-1"), ShortCheckID("relevo-gate-local-webshop-1"); got != again {
		t.Errorf("ShortCheckID not deterministic: %q vs %q", got, again)
	}
}

// TestShortTmpIDSplitsIdentities pins that distinct identities map apart:
// every live round and check needs a directory of its own.
func TestShortTmpIDSplitsIdentities(t *testing.T) {
	t.Parallel()

	seen := map[string]string{}
	add := func(id, leaf string) {
		t.Helper()
		if prev, dup := seen[leaf]; dup {
			t.Fatalf("leaf %q shared by %q and %q", leaf, prev, id)
		}
		seen[leaf] = id
	}
	for _, name := range []string{"shop", "shop2", strings.Repeat("s", 32)} {
		for _, round := range []int{1, 2, 1000} {
			id := name + "/" + string(rune('0'+round%10))
			add(id, ShortRoundID(name, round))
		}
	}
	for _, unit := range []string{
		"relevo-gate-local-webshop-1",
		"relevo-gate-abcdef12-shop-check-3",
		"relevo-gate-abcdef12-shop-check-4",
	} {
		add(unit, ShortCheckID(unit))
	}
}

// TestIsShortTmpIDRejectsLongNames pins the sweep's splitter: pre-change
// long entries must not read as short ones.
func TestIsShortTmpIDRejectsLongNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"webshop-001", "relevo-gate-local-webshop-1", "ABCDEFGH", "abc", "", "rounds"} {
		if IsShortTmpID(name) {
			t.Errorf("IsShortTmpID(%q) = true, want false", name)
		}
	}
}

// TestTmpOverBudgetRule pins the warn selection: a directory at the bound
// is quiet, one byte past it warns, and the round proceeds either way.
func TestTmpOverBudgetRule(t *testing.T) {
	t.Parallel()

	if TmpOverBudget(strings.Repeat("a", maxTmpDirLen)) {
		t.Errorf("a %d-byte dir must not warn", maxTmpDirLen)
	}
	if !TmpOverBudget(strings.Repeat("a", maxTmpDirLen+1)) {
		t.Errorf("a %d-byte dir must warn", maxTmpDirLen+1)
	}
	if maxTmpDirLen+TmpSocketSuffixLen >= 108 {
		t.Errorf("bound %d + suffix %d must stay under 108", maxTmpDirLen, TmpSocketSuffixLen)
	}
}
