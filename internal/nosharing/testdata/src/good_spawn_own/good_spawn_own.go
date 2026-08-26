package good_spawn_own

// Engine models a long-lived object whose constructor writes happen-before
// start/go even though New is exported. A keepalive goroutine only reads
// frozen own fields and follows a pointer field (db). bump writes n but is
// never reached from keepalive — package-wide allFuncs must not poison this go.
type DB struct{ n int }

func (d *DB) Query() { d.n++ }

type Engine struct {
	cfg int
	db  *DB
	n   int
}

func New() *Engine {
	e := &Engine{cfg: 1, db: &DB{}}
	e.n = 0
	go e.keepalive()
	return e
}

func (e *Engine) keepalive() {
	_ = e.cfg
	e.db.Query()
}

func (e *Engine) bump() {
	e.n++
	e.db.Query()
}

func Run() {
	// bump writes n and is never reached from keepalive. Package-wide
	// allFuncs must not treat that as a write of keepalive's captures.
	e := New()
	_ = e
}
