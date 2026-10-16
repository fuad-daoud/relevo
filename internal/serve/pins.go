package serve

import (
	"path/filepath"
	"strings"
	"sync"
)

// barePins is the server-side pin set: one reference count per bare repo path a
// bind path is currently creating or absorbing into. Its mutex is a leaf --
// nothing else is acquired while it is held -- so taking a pin never queues a
// bind behind git I/O, an owner section or admitMu.
var barePins = struct {
	sync.Mutex
	counts map[string]int
}{counts: map[string]int{}}

// pinBareRepo takes one reference on bare and returns its release. A bind path
// pins before it creates or absorbs into the bare repo and unpins only after
// the row that references it is saved -- defer covers every error path -- so
// the tick's prune, which reads the pins and the rows under the same mutex,
// sees either the pin or the row, never neither.
func pinBareRepo(bare string) func() {
	key := filepath.Clean(bare)
	barePins.Lock()
	barePins.counts[key]++
	barePins.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			barePins.Lock()
			barePins.counts[key]--
			if barePins.counts[key] <= 0 {
				delete(barePins.counts, key)
			}
			barePins.Unlock()
		})
	}
}

// pinnedLocked reports whether bare carries a pin. The caller holds barePins'
// mutex, which is what makes the prune's pin check and its row read one step.
func pinnedLocked(bare string) bool {
	return barePins.counts[filepath.Clean(bare)] > 0
}

// pinnedUnderLocked reports whether any pin lies under dir. The caller holds
// barePins' mutex.
func pinnedUnderLocked(dir string) bool {
	prefix := filepath.Clean(dir) + string(filepath.Separator)
	for key := range barePins.counts {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}
