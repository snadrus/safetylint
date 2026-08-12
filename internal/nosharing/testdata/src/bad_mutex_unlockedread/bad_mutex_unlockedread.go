package bad_mutex_unlockedread

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

func Race() int {
	c := &Counter{}
	go c.Inc() // want `shared memory .* written without channel transfer and no always-locked sync.Mutex guard`
	// Unlocked read races with Inc's write.
	return c.n
}
