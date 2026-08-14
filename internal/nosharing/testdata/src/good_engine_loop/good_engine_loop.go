package good_engine_loop

// Harmony scheduler shape: one event-loop goroutine (spawned exactly once)
// owns the schedule map; other goroutines feed it through the channel; the
// constructor fills fields before starting the loop.
type event struct {
	name string
	id   int
}

type engine struct {
	events chan event
	names  map[string]bool
	seen   map[int]bool
}

func New(names []string) *engine {
	e := &engine{
		events: make(chan event, 8),
		names:  map[string]bool{},
		seen:   map[int]bool{},
	}
	for _, n := range names {
		e.names[n] = true
	}
	e.startLoop()
	return e
}

func (e *engine) startLoop() {
	go func() {
		for ev := range e.events {
			if !e.names[ev.name] {
				continue
			}
			e.seen[ev.id] = true
		}
	}()
}

func (e *engine) Emit(name string, id int) {
	e.events <- event{name: name, id: id}
}

func Run() {
	e := New([]string{"a"})
	go e.Emit("a", 1)
	e.Emit("a", 2)
}
