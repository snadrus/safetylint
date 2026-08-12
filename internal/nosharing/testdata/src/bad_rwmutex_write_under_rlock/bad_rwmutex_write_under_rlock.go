package bad_rwmutex_write_under_rlock

import "sync"

type Counter struct {
	mu sync.RWMutex
	n  int
}

func (c *Counter) Bad() {
	c.mu.RLock()
	defer c.mu.RUnlock()
	c.n++ // write under RLock only
}

func Run() {
	c := &Counter{}
	go c.Bad() // want `shared memory .* written without channel transfer and no proven lock/atomic/partition guard`
	c.Bad()
}
