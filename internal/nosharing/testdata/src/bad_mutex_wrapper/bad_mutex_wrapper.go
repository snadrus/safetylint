package bad_mutex_wrapper

import "sync"

// Lock wrapper that does not actually take the mutex.
type state struct {
	sync.Mutex
	n int
}

func (s *state) Lock()   {}
func (s *state) Unlock() { s.Mutex.Unlock() }

var st state

func bump() {
	st.Lock()
	defer st.Unlock()
	st.n++ // want `write to package global st after goroutines may have started`
}

func Run() {
	go bump()
	bump()
}
