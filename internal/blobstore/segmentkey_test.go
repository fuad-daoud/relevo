package blobstore

import (
	"strings"
	"testing"
)

func TestSegmentKeyShape(t *testing.T) {
	t.Parallel()
	if got := SegmentKey("laptop", 1); got != "laptop/log/000000000001" {
		t.Errorf("SegmentKey(laptop, 1) = %q", got)
	}
	if got := SegmentKey("laptop", 999999999999); got != "laptop/log/999999999999" {
		t.Errorf("SegmentKey(laptop, 999999999999) = %q", got)
	}
}

func TestSegmentKeyRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		origin string
		first  int
	}{
		{"empty origin", "", 1},
		{"origin with separator", "lap/top", 1},
		{"negative first", "laptop", -1},
		{"first past twelve digits", "laptop", 1_000_000_000_000},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recover() == nil {
					t.Errorf("SegmentKey(%q, %d) did not panic", tc.origin, tc.first)
				}
			}()
			SegmentKey(tc.origin, tc.first)
		})
	}
}

func TestParseSegmentKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		origin string
		first  int
	}{
		{"laptop", 0},
		{"laptop", 1},
		{"laptop", 123456},
		{"01ARZ3NDEKTSV4RRFFQ69G5FAV", 999999999999},
		{"Ab-_9", 42},
	} {
		key := SegmentKey(tc.origin, tc.first)
		origin, first, ok := ParseSegmentKey(key)
		if !ok || origin != tc.origin || first != tc.first {
			t.Errorf("ParseSegmentKey(%q) = %q, %d, %v", key, origin, first, ok)
		}
	}
	for _, bad := range []string{
		"laptop/000000000001",                   // no log segment
		"laptop",                                // no second half at all
		"laptop/log/00000000001",                // 11 digits
		"laptop/log/0000000000001",              // 13 digits
		"laptop/log/+00000000001",               // sign
		"laptop/log/-00000000001",               // sign
		"laptop/log/00000000000a",               // non-digit
		"laptop/LOG/000000000001",               // uppercase
		"laptop/log/000000000001/",              // trailing slash
		"laptop/log/000000000001/next",          // extra path element
		Key("laptop", strings.Repeat("ab", 32)), // a body key
		SizeKey("laptop"),                       // the size key
		"lap/top/log/000000000001",              // bad origin
		"",                                      // empty
	} {
		if origin, first, ok := ParseSegmentKey(bad); ok {
			t.Errorf("ParseSegmentKey(%q) = %q, %d, true", bad, origin, first)
		}
	}
}

func TestIsBodyKeyRefusesSegmentKeys(t *testing.T) {
	t.Parallel()
	key := SegmentKey("laptop", 1)
	if IsBodyKey(key) {
		t.Errorf("IsBodyKey(%q) = true", key)
	}
	if origin, ok := SizeOrigin(key); ok || origin != "" {
		t.Errorf("SizeOrigin(%q) = %q, %v", key, origin, ok)
	}
	if IsSegmentKey(Key("laptop", strings.Repeat("ab", 32))) {
		t.Errorf("body key %q reads as a segment key", Key("laptop", strings.Repeat("ab", 32)))
	}
	if IsSegmentKey(SizeKey("laptop")) {
		t.Errorf("size key %q reads as a segment key", SizeKey("laptop"))
	}
}
