package db

import (
	"runtime"
	"sync"
	"testing"
)

// TestColumnCodecMemoryDoesNotScaleWithThreads pins the pool cap: on a machine
// with many threads, encoding and decoding large values from many goroutines
// must not leave a history buffer per thread behind. The codec is built after
// GOMAXPROCS is raised, because the pool is sized when it is built. Uncapped,
// sixteen threads retain several hundred megabytes; capped, a few tens.
func TestColumnCodecMemoryDoesNotScaleWithThreads(t *testing.T) {
	const threads = 16
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(threads))

	value := variedRunnerStream()
	for len(value) < 4<<20 {
		value = append(value, variedRunnerStream()...)
	}

	before := heapInuse()
	enc, dec := newColumnCodec()
	var wg sync.WaitGroup
	for g := 0; g < threads; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, n := range []int{64 << 10, 1 << 20, 4 << 20} {
				frame := enc.EncodeAll(value[:n], nil)
				if _, err := dec.DecodeAll(frame, nil); err != nil {
					t.Errorf("DecodeAll: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	retained := int64(heapInuse()) - int64(before)
	runtime.KeepAlive(enc)
	runtime.KeepAlive(dec)

	if limit := int64(120 << 20); retained > limit {
		t.Errorf("the codec retains %d MB after %d threads used it, want at most %d MB",
			retained>>20, threads, limit>>20)
	}
}

func heapInuse() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapInuse
}
