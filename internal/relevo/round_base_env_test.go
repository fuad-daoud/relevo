package relevo

import (
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestBaseEnvEntry(t *testing.T) {
	t.Parallel()

	if got := baseEnvEntry(store.Binding{}); got != nil {
		t.Errorf("baseEnvEntry(no base) = %v, want nil", got)
	}
	want := []string{"RELEVO_BASE=abc123"}
	if got := baseEnvEntry(store.Binding{Base: "abc123"}); !reflect.DeepEqual(got, want) {
		t.Errorf("baseEnvEntry = %v, want %v", got, want)
	}
}
