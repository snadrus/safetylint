package good_mutex_wrapper

import (
	"sync"
	"sync/atomic"
)

// Embedded mutex with custom Lock/Unlock (bookkeeping) — common pattern.
type state struct {
	sync.Mutex
	n int
}

func (s *state) Lock()   { s.Mutex.Lock() }
func (s *state) Unlock() { s.Mutex.Unlock() }

var st state

func bump() {
	st.Lock()
	defer st.Unlock()
	st.n++
}

func Run() {
	go bump()
	bump()
}

// RWMutex wrapper that also toggles an atomic flag under the write lock.
type gate struct {
	sync.RWMutex
	updating int32
	n        int
}

func (g *gate) Lock() {
	g.RWMutex.Lock()
	atomic.StoreInt32(&g.updating, 1)
}
func (g *gate) Unlock() {
	atomic.StoreInt32(&g.updating, 0)
	g.RWMutex.Unlock()
}

var g gate

func bumpGate() {
	g.Lock()
	defer g.Unlock()
	g.n++
}

func readGate() int {
	g.RLock()
	defer g.RUnlock()
	return g.n
}

func RunGate() {
	go bumpGate()
	bumpGate()
	_ = readGate()
}
