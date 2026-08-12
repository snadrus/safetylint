package bad_rwmutex

import "sync"

type Counter struct {
	mu sync.RWMutex
	n  int
}

func (c *Counter) Inc() {
	c.n++ // unlocked write
}

func Run() {
	c := &Counter{}
	go c.Inc() // want `shared memory .* written without channel transfer and no proven lock/atomic/partition guard`
	c.Inc()
}
