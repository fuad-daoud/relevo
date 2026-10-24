package doctor

import (
	"strings"
	"testing"
)

// The disk row judges plain numbers: warn strictly above the threshold, and
// say so whether or not the walk finished the tree.

func TestTmpRootCheckWarnsOverThreshold(t *testing.T) {
	t.Parallel()

	const warnBytes int64 = 1 << 20
	at := TmpRootCheck(warnBytes/2, warnBytes/4, warnBytes/4, false, warnBytes)
	if at.Severity != SevOK {
		t.Errorf("exactly at the threshold = %v, want no warning", at.Severity)
	}
	over := TmpRootCheck(warnBytes/2, warnBytes/4, warnBytes/4+1, false, warnBytes)
	if over.Severity != SevWarn {
		t.Errorf("one byte over the threshold = %v, want a warning", over.Severity)
	}
	if !strings.Contains(over.Fix, "rounds/") || !strings.Contains(over.Fix, "rm -rf") {
		t.Errorf("Fix = %q, want it to say what is swept and how to clear it", over.Fix)
	}
	if !strings.Contains(at.Detail, "rounds") {
		t.Errorf("Detail = %q, want the split of the three numbers", at.Detail)
	}
	if DefaultTmpWarnBytes != 10<<30 {
		t.Errorf("DefaultTmpWarnBytes = %d, want 10 GiB", DefaultTmpWarnBytes)
	}
}

func TestTmpRootCheckMarksTruncated(t *testing.T) {
	t.Parallel()

	trunc := TmpRootCheck(1<<20, 1<<19, 1<<18, true, DefaultTmpWarnBytes)
	if trunc.Severity != SevOK {
		t.Errorf("a truncated reading under the threshold = %v, want the truncation alone not to warn", trunc.Severity)
	}
	if !strings.Contains(trunc.Detail, "budget") {
		t.Errorf("Detail = %q, want it to mark the numbers as a floor", trunc.Detail)
	}
	full := TmpRootCheck(1<<20, 1<<19, 1<<18, false, DefaultTmpWarnBytes)
	if strings.Contains(full.Detail, "budget") {
		t.Errorf("Detail = %q, want no truncation mark on a complete reading", full.Detail)
	}
	both := TmpRootCheck(DefaultTmpWarnBytes, 1, 0, true, DefaultTmpWarnBytes)
	if both.Severity != SevWarn || !strings.Contains(both.Detail, "budget") {
		t.Errorf("truncated and over = %+v, want the warning and the mark together", both)
	}
}
