package bad_ctor_postwrite

// A write after the call that spawned the goroutine is not constructor
// setup: it races the running loop.
type engine struct {
	names map[string]int
}

func New(names []string) *engine {
	e := &engine{names: map[string]int{}}
	e.start() // want `shared memory`
	for i, n := range names {
		e.names[n] = i // races the goroutine started by start()
	}
	return e
}

func (e *engine) start() {
	go func() {
		for n := range e.names {
			_ = n
		}
	}()
}

func Run() {
	_ = New([]string{"a", "b"})
}
