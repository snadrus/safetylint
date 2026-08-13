package good_local_dyngo

import "sync"

func Run() {
	var mu sync.Mutex
	n := 0
	tick := func() {
		mu.Lock()
		n++
		mu.Unlock()
	}
	go tick()
	mu.Lock()
	n++
	mu.Unlock()
}
