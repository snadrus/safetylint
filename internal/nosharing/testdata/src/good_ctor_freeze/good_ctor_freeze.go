package good_ctor_freeze

import "sync"

// Constructor-freeze: New fills the engine's fields, then calls a helper
// that spawns the loop goroutine. The writes happen-before the call and
// therefore before the spawn inside it.
type engine struct {
	names map[string]int
	order []string

	mu   sync.Mutex
	done map[string]bool
}

func New(names []string) *engine {
	e := &engine{
		names: map[string]int{},
		done:  map[string]bool{},
	}
	for i, n := range names {
		e.names[n] = i
		e.order = append(e.order, n)
	}
	e.start()
	return e
}

func (e *engine) start() {
	go func() {
		for _, n := range e.order {
			_ = e.names[n]
			e.mu.Lock()
			e.done[n] = true
			e.mu.Unlock()
		}
	}()
}

func (e *engine) Done(n string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.done[n]
}

func Run() {
	e := New([]string{"a", "b"})
	_ = e.Done("a")
}
