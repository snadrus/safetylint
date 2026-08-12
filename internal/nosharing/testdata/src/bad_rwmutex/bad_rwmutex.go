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
	go c.Inc() // want `shared memory .* written without channel transfer and no always-locked sync.Mutex guard|shared sync.RWMutex`
	c.Inc()
}
