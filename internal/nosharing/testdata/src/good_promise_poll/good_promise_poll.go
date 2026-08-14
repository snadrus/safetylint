package good_promise_poll

import (
	"context"
	"database/sql"

	"promiselib"
)

// Curio PDPNotifyTask shape: the constructor spawns a poll goroutine on a
// struct whose fields are a ConcurrentSafe anchor (*sql.DB) and a
// mutex-guarded promise (ConcurrentSafe by imported Fact).
type task struct {
	db *sql.DB
	tf promiselib.Promise[func(int) bool]
}

func New(ctx context.Context, db *sql.DB) *task { // want New:"mayShareParams param0:read param1:read"
	t := &task{db: db}
	go t.poll(ctx)
	return t
}

func (t *task) poll(ctx context.Context) {
	for i := 0; i < 2; i++ {
		add := t.tf.Val(ctx)
		if add == nil {
			continue
		}
		_ = add(i)
		_, _ = t.db.ExecContext(ctx, "UPDATE x SET y=1")
	}
}

func Run(ctx context.Context, db *sql.DB) { // want Run:"mayShareParams param0:read param1:read"
	t := New(ctx, db)
	t.tf.Set(func(int) bool { return true })
}
