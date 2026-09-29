package harness

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShippedFileBytes(t *testing.T) {
	for _, name := range []string{
		"opencode-plugin/package.json",
		"opencode-plugin/server.ts",
		"opencode-plugin/tui.tsx",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ShippedFileBytes("opencode", name)
			if err != nil {
				t.Fatalf("ShippedFileBytes(opencode, %s): %v", name, err)
			}
			base := strings.TrimPrefix(name, "opencode-plugin/")
			want, err := os.ReadFile(filepath.Join("opencodeplugin", base))
			if err != nil {
				t.Fatalf("os.ReadFile(opencodeplugin/%s): %v", base, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("ShippedFileBytes(opencode, %s) = %d bytes, want the %d bytes of opencodeplugin/%s", name, len(got), len(want), base)
			}
		})
	}

	if _, err := ShippedFileBytes("opencode", "opencode-plugin/nope.tsx"); !errors.Is(err, ErrNoAgentDoc) {
		t.Errorf("unknown name: err = %v, want ErrNoAgentDoc", err)
	}
	if _, err := ShippedFileBytes("claude", "opencode-plugin/package.json"); !errors.Is(err, ErrNoAgentDoc) {
		t.Errorf("non-opencode kind: err = %v, want ErrNoAgentDoc", err)
	}
	if _, err := ShippedFileBytes("nope", "opencode-plugin/package.json"); !errors.Is(err, ErrNoAgentDoc) {
		t.Errorf("unknown kind: err = %v, want ErrNoAgentDoc", err)
	}
}
