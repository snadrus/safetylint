package good_rwmutex

import "sync"

type Counter struct { // want Counter:"concurrentSafe"
	mu sync.RWMutex
	n  int
}

func (c *Counter) Inc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *Counter) Get() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.n
}

func Run() int {
	c := &Counter{}
	go c.Inc()
	c.Inc()
	return c.Get()
}
