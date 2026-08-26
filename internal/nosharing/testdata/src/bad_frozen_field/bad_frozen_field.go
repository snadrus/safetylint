package bad_frozen_field

import "sync"

type S struct {
	mu sync.Mutex
	n  int
	db *int
}

func New() *S {
	x := 0
	s := &S{db: &x}
	go func() { // want `shared memory .* written without channel transfer`
		s.mu.Lock()
		s.n++
		s.mu.Unlock()
		s.db = new(int)
	}()
	return s
}

func Run() {
	_ = New()
}
