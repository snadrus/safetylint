package good_mutexstruct

import "sync"

type Counter struct {
	mu sync.Mutex
	n  int
}

func (c *Counter) Inc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *Counter) Get() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func Run() int {
	c := &Counter{}
	go c.Inc()
	c.Inc()
	return c.Get()
}
