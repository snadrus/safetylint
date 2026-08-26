package good_global_atomic

import "sync/atomic"

var ready atomic.Bool
var slot atomic.Pointer[int]

func Run() {
	go func() {
		ready.Store(true)
		n := 1
		slot.Store(&n)
	}()
	for !ready.Load() {
	}
	_ = slot.Load()
}
