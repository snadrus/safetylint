package good_nested_promise

import (
	"context"
	"net/http"
	"sync"
	"time"
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

type DB struct{ n int }

func (d *DB) Query() { _ = d.n }

type Task struct {
	db     *DB
	TF     Promise[func()]
	client *http.Client
}

func start(ctx context.Context) *Task {
	t := &Task{db: &DB{}, client: &http.Client{Timeout: time.Second}}
	go t.poll(ctx)
	return t
}

func (t *Task) poll(ctx context.Context) {
	_ = t.db
	_ = t.client
	fn := t.TF.Val(ctx)
	if fn != nil {
		fn()
	}
}

func (t *Task) Adder(fn func()) {
	t.TF.Set(fn)
}

func Run() {
	t := start(context.Background())
	t.Adder(func() {})
}
