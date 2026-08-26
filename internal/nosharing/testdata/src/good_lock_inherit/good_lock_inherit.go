package good_lock_inherit

import "sync"

type Throttle struct {
	mu    sync.Mutex
	state map[string]int
}

func (t *Throttle) cleanupIPLocked(ip string) {
	delete(t.state, ip)
}

func (t *Throttle) cleanupAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for ip := range t.state {
		t.cleanupIPLocked(ip)
	}
}

func (t *Throttle) RunCleanup() {
	t.cleanupAll()
}

func Run() {
	t := &Throttle{state: map[string]int{"a": 1}}
	go t.RunCleanup()
}
