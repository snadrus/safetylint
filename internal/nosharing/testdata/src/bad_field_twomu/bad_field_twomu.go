package bad_field_twomu

import "sync"

// Field a is touched under two different mutexes — not a consistent mapping.
type S struct {
	mu1, mu2 sync.Mutex
	a, b     int
}

func (s *S) A1() {
	s.mu1.Lock()
	s.a++
	s.mu1.Unlock()
}

func (s *S) A2() {
	s.mu2.Lock()
	s.a++
	s.mu2.Unlock()
}

func Run() {
	s := &S{}
	go s.A1() // want `shared memory .* written without channel transfer`
	s.A2()
}
