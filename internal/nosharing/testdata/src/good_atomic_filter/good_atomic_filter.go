package good_atomic_filter

import (
	"context"
	"sync/atomic"
)

type Filter struct { // want Filter:"concurrentSafe"
	ptr atomic.Pointer[map[string]struct{}]
	ctx context.Context
}

func (f *Filter) refresh() {
	_ = f.ctx
	m := map[string]struct{}{"a": {}}
	f.ptr.Store(&m)
}

func Run() {
	f := &Filter{ctx: context.Background()}
	m := map[string]struct{}{}
	f.ptr.Store(&m)
	go f.refresh()
}
