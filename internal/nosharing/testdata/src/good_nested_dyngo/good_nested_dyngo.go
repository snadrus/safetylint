package good_nested_dyngo

import "sync"

func Run() {
	var mu sync.Mutex
	n := 0
	tick := func() {
		mu.Lock()
		n++
		mu.Unlock()
	}
	outer := func() {
		inner := func() {
			go tick()
		}
		inner()
	}
	outer()
	mu.Lock()
	n++
	mu.Unlock()
}
