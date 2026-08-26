package good_cancel_unlock

import (
	"context"
	"sync"
)

type Counter struct {
	mu sync.Mutex
	n  int
}

func Run() {
	c := &Counter{}
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	go func() {
		<-ctx.Done()
		c.mu.Unlock()
	}()
	c.n++
	cancel()
}
