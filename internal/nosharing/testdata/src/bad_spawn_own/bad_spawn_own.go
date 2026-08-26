package bad_spawn_own

// Two goroutines write the same own field of a shared Engine. That is a
// real race and must still fail after spawn-scoped analysis.
type Engine struct {
	n int
}

func New() *Engine {
	e := &Engine{}
	go e.inc() // want `shared memory .* written without channel transfer`
	go e.inc() // want `shared memory .* written without channel transfer`
	return e
}

func (e *Engine) inc() { e.n++ }

func Run() { _ = New() }
