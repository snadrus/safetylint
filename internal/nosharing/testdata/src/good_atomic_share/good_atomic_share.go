package good_atomic_share

import "sync/atomic"

type Filter struct { // want Filter:"concurrentSafe"
	ptr atomic.Pointer[int]
}

func (f *Filter) Arm() {
	x := 1
	f.ptr.Store(&x)
}

func Run() {
	f := &Filter{}
	go f.Arm()
	f.Arm()
}
