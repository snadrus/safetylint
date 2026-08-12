package bad_rwmutex

import "sync"

type Counter struct {
	mu sync.RWMutex
	n  int
}

func (c *Counter) Inc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func Run() {
	c := &Counter{}
	go c.Inc() // want `RWMutex-guarded sharing refused — sync.Mutex is just as fast`
	c.Inc()
}
