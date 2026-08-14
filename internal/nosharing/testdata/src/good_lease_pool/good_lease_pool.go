package good_lease_pool

import "sync"

func Run() {
	var mu sync.Mutex
	pool := [][]byte{make([]byte, 8), make([]byte, 8)}
	var wg sync.WaitGroup
	wg.Add(1)
	mu.Lock()
	buf := pool[len(pool)-1]
	pool = pool[:len(pool)-1]
	mu.Unlock()
	go func() {
		defer wg.Done()
		buf[0] = 1
		mu.Lock()
		pool = append(pool, buf)
		mu.Unlock()
	}()
	wg.Wait()
}
