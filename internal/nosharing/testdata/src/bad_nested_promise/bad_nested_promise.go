package bad_nested_promise

import (
	"context"
	"net/http"
	"time"
)

type DB struct{ n int }

func (d *DB) Query() { _ = d.n }

type Task struct {
	db     *DB
	TF     func()
	client *http.Client
}

func start(ctx context.Context) *Task {
	t := &Task{db: &DB{}, client: &http.Client{Timeout: time.Second}}
	go t.poll(ctx) // want `shared memory .* written without channel transfer`
	return t
}

func (t *Task) poll(ctx context.Context) {
	_ = t.db
	_ = t.client
	if t.TF != nil {
		t.TF()
	}
}

func (t *Task) Adder(fn func()) {
	t.TF = fn
}

func Run() {
	t := start(context.Background())
	t.TF = func() {}
	t.Adder(func() {})
}
