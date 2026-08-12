package good_trylock

import "sync"

// TryLock is accepted only when the true result is verified on the path
// that touches the guarded field.
type S struct {
	mu sync.Mutex
	n  int
}

func tryInc(s *S) {
	if s.mu.TryLock() {
		s.n++
		s.mu.Unlock()
	}
}

func tryIncNeg(s *S) {
	ok := s.mu.TryLock()
	if !ok {
		return
	}
	s.n++
	s.mu.Unlock()
}

func spawn(s *S) {
	go tryInc(s)
}

func Run() {
	s := &S{}
	spawn(s)
	tryInc(s)
	tryIncNeg(s)
}
