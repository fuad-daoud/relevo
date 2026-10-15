package db

import (
	"runtime"
	"testing"
)

// TestColumnCodecMemoryDoesNotScaleWithThreads pins the pool cap. The pool
// hands its coders out in turn, so even one caller at a time -- the daemon's
// writes are serialized -- grows every pooled coder's history to the largest
// value it has seen. The codec is built after GOMAXPROCS is raised, because the
// pool is sized when it is built. Uncapped, sixteen threads retain several
// hundred megabytes; capped, a few tens.
func TestColumnCodecMemoryDoesNotScaleWithThreads(t *testing.T) {
	const threads = 16
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(threads))

	value := variedRunnerStream()
	for len(value) < 4<<20 {
		value = append(value, variedRunnerStream()...)
	}

	before := heapInuse()
	enc, dec := newColumnCodec()
	for i := 0; i < 2*threads; i++ {
		frame := enc.EncodeAll(value, nil)
		if _, err := dec.DecodeAll(frame, nil); err != nil {
			t.Fatalf("DecodeAll: %v", err)
		}
	}
	retained := int64(heapInuse()) - int64(before)
	runtime.KeepAlive(enc)
	runtime.KeepAlive(dec)

	if limit := int64(120 << 20); retained > limit {
		t.Errorf("the codec retains %d MB after %d calls, want at most %d MB",
			retained>>20, 2*threads, limit>>20)
	}
}

func heapInuse() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapInuse
}
