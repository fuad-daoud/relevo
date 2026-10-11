package blobstore

import (
	"strings"
	"testing"
)

func TestSizeKeyIsNotABody(t *testing.T) {
	t.Parallel()
	key := SizeKey("01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if IsBodyKey(key) {
		t.Fatalf("size key %q reads as a body key", key)
	}
	if origin, ok := SizeOrigin(key); !ok || origin != "01ARZ3NDEKTSV4RRFFQ69G5FAV" {
		t.Errorf("SizeOrigin(%q) = %q, %v", key, origin, ok)
	}
	body := Key("m1", strings.Repeat("ab", 32))
	if !IsBodyKey(body) {
		t.Errorf("body key %q does not read as a body", body)
	}
	if _, ok := SizeOrigin(body); ok {
		t.Errorf("body key %q reads as a size key", body)
	}
	for _, bad := range []string{"m1", "m1/", "m1/" + strings.Repeat("AB", 32), "m1/" + strings.Repeat("a", 63), "a/b/" + strings.Repeat("a", 64)} {
		if IsBodyKey(bad) {
			t.Errorf("IsBodyKey(%q) = true", bad)
		}
	}
}
