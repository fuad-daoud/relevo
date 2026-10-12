package sync

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestDeriveKeyIsDeterministicPerSaltAndParams(t *testing.T) {
	t.Parallel()
	fast := KDFParams{Time: 1, MemoryKiB: 19456, Threads: 1}
	salt := []byte("0123456789abcdef")
	a := DeriveKey([]byte("pass"), salt, fast)
	if len(a) != 32 {
		t.Fatalf("key length = %d, want 32", len(a))
	}
	if !bytes.Equal(a, DeriveKey([]byte("pass"), salt, fast)) {
		t.Error("the same inputs derived two keys")
	}
	for name, other := range map[string][]byte{
		"salt":       DeriveKey([]byte("pass"), []byte("fedcba9876543210"), fast),
		"params":     DeriveKey([]byte("pass"), salt, KDFParams{Time: 2, MemoryKiB: 19456, Threads: 1}),
		"passphrase": DeriveKey([]byte("other"), salt, fast),
	} {
		if bytes.Equal(a, other) {
			t.Errorf("a different %s derived the same key", name)
		}
	}
	if bytes.Equal(KeyCheck(a), KeyCheck(other(a))) {
		t.Error("two keys share a check")
	}
}

func other(k []byte) []byte { return append([]byte{k[0] ^ 1}, k[1:]...) }

func TestKDFParamsRefuseOutOfBounds(t *testing.T) {
	t.Parallel()
	if got, err := ParseKDF(DefaultKDF.String()); err != nil || got != DefaultKDF {
		t.Fatalf("ParseKDF(default) = %+v, %v", got, err)
	}
	bad := []string{
		fmt.Sprintf("argon2id;t=%d;m=65536;p=4", 0),
		fmt.Sprintf("argon2id;t=%d;m=65536;p=4", 5),
		fmt.Sprintf("argon2id;t=3;m=%d;p=4", 19455),
		fmt.Sprintf("argon2id;t=3;m=%d;p=4", 262145),
		fmt.Sprintf("argon2id;t=3;m=65536;p=%d", 0),
		fmt.Sprintf("argon2id;t=3;m=65536;p=%d", 9),
		"argon2id",
		"scrypt;t=3;m=65536;p=4",
		"argon2id;t=x;m=65536;p=4",
	}
	for _, s := range bad {
		if _, err := ParseKDF(s); !errors.Is(err, ErrKDFParams) {
			t.Errorf("ParseKDF(%q) = %v, want ErrKDFParams", s, err)
		}
	}
	for _, s := range []string{"argon2id;t=1;m=19456;p=1", "argon2id;t=4;m=262144;p=8"} {
		if _, err := ParseKDF(s); err != nil {
			t.Errorf("ParseKDF(%q) = %v, want the bound accepted", s, err)
		}
	}
}
