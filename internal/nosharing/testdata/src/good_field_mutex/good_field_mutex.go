package good_field_mutex

import "sync"

type diff struct {
	cdmx  sync.Mutex
	notes map[int]func()
}

type box struct {
	sync.RWMutex
	diff
	val int
}

func (b *box) Lock() { b.RWMutex.Lock() }
func (b *box) Unlock() {
	b.cdmx.Lock()
	b.RWMutex.Unlock()
	defer b.cdmx.Unlock()
	b.notes[0] = nil
}

var g = box{diff: diff{notes: map[int]func(){}}}

func set() {
	g.Lock()
	defer g.Unlock()
	g.val++
}

func onChange() {
	g.cdmx.Lock()
	defer g.cdmx.Unlock()
	g.notes[1] = func() {}
}

func Run() {
	go set()
	go onChange()
	set()
	onChange()
}
