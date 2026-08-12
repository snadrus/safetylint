package good_global_atomic

import "sync/atomic"

var ready atomic.Bool

func Run() {
	go func() {
		ready.Store(true)
	}()
	for !ready.Load() {
	}
}
