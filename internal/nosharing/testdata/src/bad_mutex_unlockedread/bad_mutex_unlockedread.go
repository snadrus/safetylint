package bad_mutex_unlockedread

import "sync"

type Counter struct { // want Counter:"concurrentSafe"
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
	go c.Inc() // want `shared memory .* written without channel transfer and no proven lock/atomic/partition guard`
	// Unlocked read races with Inc's write.
	return c.n
}
