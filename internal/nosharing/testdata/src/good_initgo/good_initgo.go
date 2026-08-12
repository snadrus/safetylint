// want package:"hotGlobals st:write"
package good_initgo

import "sync"

type state struct {
	mu sync.Mutex
	n  int
}

var st state

func init() {
	go func() {
		st.mu.Lock()
		st.n++
		st.mu.Unlock()
	}()
}

func Run() {
	st.mu.Lock()
	st.n++
	st.mu.Unlock()
}
