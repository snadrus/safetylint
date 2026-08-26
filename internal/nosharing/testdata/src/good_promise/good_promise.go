package good_promise

import (
	"context"
	"sync"
)

type Promise[T any] struct {
	val  T
	done chan struct{}
	mu   sync.Mutex
}

func (p *Promise[T]) Set(val T) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.val = val
	if p.done == nil {
		p.done = make(chan struct{})
	}
	close(p.done)
}

func (p *Promise[T]) Val(ctx context.Context) T {
	p.mu.Lock()
	if p.done == nil {
		p.done = make(chan struct{})
	}
	p.mu.Unlock()
	select {
	case <-ctx.Done():
		return *new(T)
	case <-p.done:
		p.mu.Lock()
		val := p.val
		p.mu.Unlock()
		return val
	}
}

func Run() {
	p := &Promise[int]{}
	p.Set(1)
	go func() {
		_ = p.Val(context.Background())
	}()
	_ = p.Val(context.Background())
}
