package bad_cancel_unlock

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
	go func() { // want `shared memory .* written without channel transfer`
		<-ctx.Done()
		c.n++
		c.mu.Unlock()
	}()
	cancel()
}
