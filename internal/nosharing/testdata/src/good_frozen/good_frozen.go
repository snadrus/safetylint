package good_frozen

import "sync"

type db struct{ id int }

type Svc struct { // want Svc:"concurrentSafe"
	db *db // unexported, set only in the constructor
	mu sync.Mutex
	n  int
}

func New() *Svc {
	s := &Svc{db: &db{id: 1}}
	go s.loop()
	return s
}

func (s *Svc) loop() {
	_ = s.db.id // frozen read — must not poison the mutex proof
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
}

func Run() *Svc {
	return New()
}
