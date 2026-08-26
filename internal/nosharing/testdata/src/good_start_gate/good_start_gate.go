package good_start_gate

import "sync"

type Sched struct {
	lk        sync.Mutex
	callbacks []func()
	started   bool
	wlk       sync.Mutex
	watchers  []func()
}

func (s *Sched) AddHandler(ch func()) {
	s.lk.Lock()
	defer s.lk.Unlock()
	if s.started {
		return
	}
	s.callbacks = append(s.callbacks, ch)
}

func (s *Sched) AddWatcher(ch func()) {
	s.wlk.Lock()
	defer s.wlk.Unlock()
	if s.started {
		return
	}
	s.watchers = append(s.watchers, ch)
}

func (s *Sched) Run() {
	s.lk.Lock()
	s.started = true
	s.lk.Unlock()
	for _, cb := range s.callbacks {
		cb()
	}
	for _, w := range s.watchers {
		w()
	}
}

func Start() {
	s := &Sched{}
	s.AddHandler(func() {})
	s.AddWatcher(func() {})
	go s.Run()
}
