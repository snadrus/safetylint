package good_globalmutex

import "sync"

type state struct {
	mu sync.Mutex
	n  int
}

var st state

func Bump() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.n++
}

func Run() {
	go Bump()
	Bump()
}
