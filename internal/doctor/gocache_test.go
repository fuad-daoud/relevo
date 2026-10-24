package doctor

import (
	"strings"
	"testing"
)

func TestGoCacheCheckWarnsOverThreshold(t *testing.T) {
	t.Parallel()

	const warn int64 = 1 << 20
	if c := GoCacheCheck("/c", warn, false, warn); c.Severity != SevOK {
		t.Errorf("exactly at the threshold = %v, want no warning", c.Severity)
	}
	over := GoCacheCheck("/c", warn+1, false, warn)
	if over.Severity != SevWarn || !strings.Contains(over.Fix, "go clean -cache") {
		t.Errorf("one byte over = %+v, want a warning with the fix", over)
	}
	if DefaultGoCacheWarnBytes != 50<<30 {
		t.Errorf("DefaultGoCacheWarnBytes = %d, want 50 GiB", DefaultGoCacheWarnBytes)
	}
	if c := GoCacheCheck("/c", 1, true, warn); !strings.Contains(c.Detail, "budget") {
		t.Errorf("Detail = %q, want the floor mark", c.Detail)
	}
}

func TestGoCacheCheckNoGoIsInfo(t *testing.T) {
	t.Parallel()

	c := GoCacheCheck("", 0, false, DefaultGoCacheWarnBytes)
	if c.Severity != SevInfo || c.Name != "go cache" {
		t.Errorf("no go = %+v, want an informational go cache row", c)
	}
}

func TestTrimpathCheckShowsValue(t *testing.T) {
	t.Parallel()

	if c := TrimpathCheck(true); !strings.Contains(c.Detail, "on") || c.Severity != SevInfo {
		t.Errorf("on = %+v", c)
	}
	if c := TrimpathCheck(false); !strings.Contains(c.Detail, "off") {
		t.Errorf("off = %+v", c)
	}
}
