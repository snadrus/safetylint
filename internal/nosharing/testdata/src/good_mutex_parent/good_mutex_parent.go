package good_mutex_parent

import "sync"

type Inner struct {
	n int
}

type Outer struct {
	mu    sync.Mutex
	inner Inner
}

func (o *Outer) Inc() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.inner.n++
}

func Run() {
	o := &Outer{}
	go o.Inc()
	o.Inc()
}
