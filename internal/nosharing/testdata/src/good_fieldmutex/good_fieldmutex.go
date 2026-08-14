package good_fieldmutex

import "sync"

// Different fields may use different mutexes; each field always uses the same one.
type Cache struct { // want Cache:"concurrentSafe"
	rlk sync.Mutex
	r   int
	wlk sync.Mutex
	w   int
}

func (c *Cache) IncR() {
	c.rlk.Lock()
	defer c.rlk.Unlock()
	c.r++
}

func (c *Cache) IncW() {
	c.wlk.Lock()
	defer c.wlk.Unlock()
	c.w++
}

func Run() {
	c := &Cache{}
	go c.IncR()
	go c.IncW()
	c.IncR()
	c.IncW()
}
