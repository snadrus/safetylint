package bad_mutex_partial

import "sync"

type Counter struct {
	mu sync.Mutex
	n  int
}

func (c *Counter) IncLocked() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *Counter) IncBare() {
	c.n++ // unlocked write
}

func Race() {
	c := &Counter{}
	go c.IncLocked() // want `shared memory .* written without channel transfer and no proven lock/atomic/partition guard`
	c.IncBare()
}
