package good_iface_arg

import "sync"

type T struct {
	mu sync.Mutex
	n  int
}

type I interface {
	Use(*T)
}

type impl struct{}

func (impl) Use(t *T) {}

func Run() {
	t := &T{}
	var i I = impl{}
	go func() {
		i.Use(t)
		t.mu.Lock()
		t.n++
		t.mu.Unlock()
	}()
	t.mu.Lock()
	t.n++
	t.mu.Unlock()
}
