package store

import (
	"bytes"
	"fmt"
	"testing"
)

type formatCase struct {
	name      string
	mutate    func(*Binding)
	wantActor string // the "actor" fragment the record must carry
	verify    func(*testing.T, Binding)
}

func formatCases() []formatCase {
	return []formatCase{
		{
			name:      "a builder binding names the builder actor",
			mutate:    func(*Binding) {},
			wantActor: `"actor":"builder"`,
			verify: func(t *testing.T, got Binding) {
				if got.Role != "builder" {
					t.Errorf("builder Role = %q, want builder", got.Role)
				}
			},
		},
		{
			name:      "a custom actor is written as itself",
			mutate:    func(b *Binding) { b.Role = "ui-builder" },
			wantActor: `"actor":"ui-builder"`,
			verify: func(t *testing.T, got Binding) {
				if got.Role != "ui-builder" {
					t.Errorf("Role = %q, want ui-builder", got.Role)
				}
			},
		},
		{
			name: "RemoteLive is a poll cache and does not change the actor",
			mutate: func(b *Binding) {
				b.Builder.Mode = ModeRemote
				b.Builder.RemoteLive = &LiveFacts{PID: 4242}
			},
			wantActor: `"actor":"builder"`,
			verify: func(t *testing.T, got Binding) {
				if got.Builder.RemoteLive == nil || got.Builder.RemoteLive.PID != 4242 {
					t.Errorf("RemoteLive did not round-trip: %+v", got.Builder.RemoteLive)
				}
			},
		},
		{
			name: "StreamStart is a byte offset and does not change the actor",
			mutate: func(b *Binding) {
				b.Builder.Mode = ModeHeadless
				b.Builder.StreamStart = 1024
			},
			wantActor: `"actor":"builder"`,
			verify: func(t *testing.T, got Binding) {
				if got.Builder.StreamStart != 1024 {
					t.Errorf("StreamStart did not round-trip: %d", got.Builder.StreamStart)
				}
			},
		},
	}
}

// TestSaveWritesCurrentFormatAndAlwaysNamesTheActor pins the A5 format rule:
// every record is at the current format (so an older relevo refuses it) and
// every record names its actor, the empty (builder) one as "builder".
func TestSaveWritesCurrentFormatAndAlwaysNamesTheActor(t *testing.T) {
	for _, tc := range formatCases() {
		t.Run(tc.name, func(t *testing.T) {
			s := New(t.TempDir())
			b := newBinding("webshop", "/home/dev/projects/webshop")
			tc.mutate(&b)
			if err := s.Save(b); err != nil {
				t.Fatalf("Save: %v", err)
			}
			raw := bindingRecordJSON(t, s, b.Name)
			checkBindingKey(t, raw, "format", fmt.Sprintf(`"format":%d`, BindingFormat))
			checkBindingKey(t, raw, "actor", tc.wantActor)

			got, err := s.Load(b.Name)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			tc.verify(t, got)
		})
	}
}

// checkBindingKey asserts raw carries want -- a fragment, or "" for a key that
// must be absent.
func checkBindingKey(t *testing.T, raw []byte, key, want string) {
	t.Helper()
	if want == "" {
		if bytes.Contains(raw, []byte(`"`+key+`"`)) {
			t.Errorf("record must carry no %s key:\n%s", key, raw)
		}
		return
	}
	if !bytes.Contains(raw, []byte(want)) {
		t.Errorf("record lacks %s:\n%s", want, raw)
	}
}
