package bad_engine_twoloops

// Two spawned loops writing the same field: no single owner.
type engine struct {
	events chan int
	n      int
}

func New() *engine {
	e := &engine{events: make(chan int, 8)}
	go func() { // want `shared memory`
		for range e.events {
			e.n++
		}
	}()
	go func() { // want `shared memory`
		for range e.events {
			e.n--
		}
	}()
	return e
}

func Run() {
	e := New()
	e.events <- 1
}
