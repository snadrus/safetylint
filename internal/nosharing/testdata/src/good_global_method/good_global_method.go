package good_global_method

import "sync"

// Sharing into a goroutine that only touches a mutex-guarded global via
// methods must not report shared_mem on the method receiver alias; freeze
// + tied mutex cover the global.
type state struct {
	mu sync.Mutex
	n  int
}

var st state

func (s *state) bump() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
}

func helper() {
	st.bump()
}

func Run() {
	go helper()
	helper()
}
