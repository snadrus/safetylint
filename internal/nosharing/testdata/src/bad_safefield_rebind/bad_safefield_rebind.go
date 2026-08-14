package bad_safefield_rebind

import "database/sql"

// Rebinding a ConcurrentSafe-typed field cell is still a racy write: the
// per-field safety of *sql.DB does not cover replacing the pointer itself.
type task struct {
	db *sql.DB
}

func open() *sql.DB {
	db, _ := sql.Open("x", "")
	return db
}

func Run() {
	t := &task{db: open()}
	go func() { // want `shared memory`
		_ = t.db.Ping()
	}()
	t.db = open()
}
