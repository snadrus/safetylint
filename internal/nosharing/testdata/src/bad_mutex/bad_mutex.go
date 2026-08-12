package bad_mutex

import "sync"

func SharedMap() {
	var mu sync.Mutex
	m := map[string]int{}
	go func() { // want `shared memory .* written without channel transfer and no always-locked sync.Mutex guard`
		mu.Lock()
		m["a"] = 1
		mu.Unlock()
	}()
	mu.Lock()
	m["b"] = 2
	mu.Unlock()
}
