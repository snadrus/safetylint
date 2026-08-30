package good_watcher_method

import (
	"sync"
	"time"
)

type replaceTrigger struct {
	Height    int
	Timestamp time.Time
}

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
	triggerCh     chan struct{}
	triggerMu     sync.Mutex
	latestTrigger replaceTrigger
}

func New(s *Sched) *Replacer {
	t := &Replacer{triggerCh: make(chan struct{}, 1)}
	s.AddWatcher(t.onHead)
	go t.run()
	return t
}

func (t *Replacer) onHead(h int) {
	t.triggerMu.Lock()
	t.latestTrigger = replaceTrigger{Height: h, Timestamp: time.Now()}
	t.triggerMu.Unlock()
	select {
	case t.triggerCh <- struct{}{}:
	default:
	}
}

func (t *Replacer) run() {
	<-t.triggerCh
	t.triggerMu.Lock()
	_ = t.latestTrigger
	t.triggerMu.Unlock()
}

func Run() {
	s := &Sched{}
	t := New(s)
	t.onHead(2)
	s.Fire()
}
