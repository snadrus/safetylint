package bad_watcher_method

import "sync"

type Sched struct {
	fn func(int)
}

func (s *Sched) AddWatcher(fn func(int)) {
	s.fn = fn
}

func (s *Sched) Fire() {
	if s.fn != nil {
		s.fn(1)
	}
}

type Replacer struct {
	triggerCh chan struct{}
	triggerMu sync.Mutex
	n         int
}

func New(s *Sched) *Replacer {
	t := &Replacer{triggerCh: make(chan struct{}, 1)}
	s.AddWatcher(t.onHead)
	go t.run() // want `shared memory .* written without channel transfer`
	return t
}

func (t *Replacer) onHead(h int) {
	t.n = h
}

func (t *Replacer) run() {
	<-t.triggerCh
	_ = t.n
}

func Run() {
	s := &Sched{}
	t := New(s)
	t.onHead(2)
	s.Fire()
}
