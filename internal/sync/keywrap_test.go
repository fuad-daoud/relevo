package sync

import (
	"bytes"
	"errors"
	"testing"
)

func keywrapKeys(t *testing.T) (k1, k2, k3 []byte) {
	t.Helper()
	return DeriveKey([]byte("first"), []byte("salt-one-12345678"), DefaultKDF),
		DeriveKey([]byte("second"), []byte("salt-two-12345678"), DefaultKDF),
		DeriveKey([]byte("third"), []byte("salt-three-234567"), DefaultKDF)
}

// TestKeyWrapRoundTripSealsAndOpensAnEpochKey pins the wrapped row's shape: the
// bytes WrapKey returns are the one value key_wrapped.<epoch> stores, the key
// that replaced the wrapped one opens it, and the same inputs wrap to the same
// bytes.
func TestKeyWrapRoundTripSealsAndOpensAnEpochKey(t *testing.T) {
	t.Parallel()
	k1, k2, k3 := keywrapKeys(t)

	chain := []struct {
		newer []byte
		epoch int
		key   []byte
	}{
		{newer: k3, epoch: 2, key: k2},
		{newer: k2, epoch: 1, key: k1},
	}
	for _, link := range chain {
		row, err := WrapKey(link.newer, link.epoch, link.key)
		if err != nil {
			t.Fatalf("WrapKey epoch %d: %v", link.epoch, err)
		}
		got, err := UnwrapKey(link.newer, link.epoch, row)
		if err != nil {
			t.Fatalf("UnwrapKey epoch %d: %v", link.epoch, err)
		}
		if !bytes.Equal(got, link.key) {
			t.Errorf("UnwrapKey epoch %d = %x, want the key that was sealed", link.epoch, got)
		}
		again, err := WrapKey(link.newer, link.epoch, link.key)
		if err != nil || !bytes.Equal(again, row) {
			t.Errorf("wrapping epoch %d twice produced two rows (%x, %x), want one", link.epoch, again, row)
		}
	}
}

// TestKeyWrapRefusesAWrongEpoch pins the associated data: the row is bound to
// the pair (epoch, epoch+1), so a row opened at another slot in the chain fails
// to authenticate instead of handing back a key that belongs elsewhere.
func TestKeyWrapRefusesAWrongEpoch(t *testing.T) {
	t.Parallel()
	k1, k2, _ := keywrapKeys(t)
	row, err := WrapKey(k2, 1, k1)
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}
	for _, epoch := range []int{0, 2, 3} {
		if _, err := UnwrapKey(k2, epoch, row); !errors.Is(err, ErrKDFParams) {
			t.Errorf("UnwrapKey claiming epoch %d = %v, want ErrKDFParams", epoch, err)
		}
	}
}

// TestKeyWrapRefusesAnOldKey pins the chain's direction: the wrap is derived
// from the key that replaced the sealed one, so a machine holding only an old
// epoch's key cannot open the row that holds it -- and no old key opens
// anything newer than itself.
func TestKeyWrapRefusesAnOldKey(t *testing.T) {
	t.Parallel()
	k1, k2, k3 := keywrapKeys(t)
	row, err := WrapKey(k2, 1, k1)
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}
	for name, newer := range map[string][]byte{"the wrapped key itself": k1, "an unrelated key": k3} {
		if _, err := UnwrapKey(newer, 1, row); !errors.Is(err, ErrKDFParams) {
			t.Errorf("UnwrapKey under %s = %v, want ErrKDFParams", name, err)
		}
	}
	// A row cut short is not a row that opens to something else either.
	if _, err := UnwrapKey(k2, 1, row[:len(row)-1]); !errors.Is(err, ErrKDFParams) {
		t.Errorf("UnwrapKey of a truncated row = %v, want ErrKDFParams", err)
	}
}
