package bad_atomic_mix

import "sync/atomic"

type Filter struct {
	ptr atomic.Pointer[int]
	n   int
}

func (f *Filter) Arm() {
	x := 1
	f.ptr.Store(&x)
	f.n++ // plain store mixed with atomics
}

func Run() {
	f := &Filter{}
	go f.Arm() // want `shared memory .* written without channel transfer and no proven lock/atomic/partition guard`
	f.Arm()
}
