package good_frozen_field

import "sync"

type S struct {
	mu sync.Mutex
	n  int
	db *int
}

func New() *S {
	x := 0
	s := &S{db: &x}
	go func() {
		_ = *s.db
		s.mu.Lock()
		s.n++
		s.mu.Unlock()
	}()
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
	return s
}

func Run() {
	_ = New()
}
