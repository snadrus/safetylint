package good_local_freemu

import "sync"

// Local free-standing mutex covering all concurrent map accesses.
func SharedMap() {
	var mu sync.Mutex
	m := map[string]int{}
	go func() {
		mu.Lock()
		m["a"] = 1
		mu.Unlock()
	}()
	mu.Lock()
	m["b"] = 2
	mu.Unlock()
}
