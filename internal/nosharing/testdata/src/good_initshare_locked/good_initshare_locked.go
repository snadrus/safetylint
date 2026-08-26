package good_initshare_locked

import "sync"

// Obj is written during init, then shared with a goroutine that only
// mutates under the object's tied mutex.
type Obj struct {
	mu sync.Mutex
	n  int
}

func (o *Obj) loop() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.n++
}

func (o *Obj) setup() {
	o.n = 1
}

func New() *Obj {
	o := &Obj{}
	o.setup()
	go o.loop()
	return o
}

func Run() {
	o := &Obj{}
	o.n = 2
	go o.loop()
}
