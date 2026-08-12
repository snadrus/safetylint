package bad_twomutex

import "sync"

// Two mutexes in one struct are not interchangeable: every touchpoint of n
// must share one tied mutex.
type S struct {
	mu1, mu2 sync.Mutex
	n        int
}

func bump(s *S, useFirst bool) {
	if useFirst {
		s.mu1.Lock()
		s.n++
		s.mu1.Unlock()
	} else {
		s.mu2.Lock()
		s.n++
		s.mu2.Unlock()
	}
}

func spawn(s *S) {
	go bump(s, true) // want `shared memory .* written without channel transfer`
}

func Run() {
	s := &S{}
	spawn(s)
	bump(s, false)
}
