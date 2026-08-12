package bad_trylock

import "sync"

// TryLock does not count as an acquire unless its boolean result is checked.
type S struct {
	mu sync.Mutex
	n  int
}

func bad(s *S) {
	s.mu.TryLock() // ignore result
	s.n++
}

func good(s *S) {
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
}

func spawn(s *S) {
	go good(s) // want `shared memory .* written without channel transfer`
}

func Run() {
	s := &S{}
	spawn(s) // want `shared memory from Fact-bearing call`
	bad(s)
}
