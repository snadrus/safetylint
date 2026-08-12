package sharemu

import "sync"

type S struct {
	Mu sync.Mutex
	N  int
}

func bump(s *S) {
	s.Mu.Lock()
	s.N++
	s.Mu.Unlock()
}

// Start retains s under tied mutex Mu.
func Start(s *S) { // want Start:"mayShareParams param0:write"
	go bump(s)
}
