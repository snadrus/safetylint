package bad_wg_localmu_unlocked

import "sync"

func Race() int {
	var mu sync.Mutex
	var x int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // want `shared memory`
		defer wg.Done()
		x = 1 // unlocked write
		_ = mu
	}()
	go func() { // want `shared memory`
		mu.Lock()
		x = 2
		mu.Unlock()
	}()
	wg.Wait()
	return x
}

func Run() {
	go func() { _ = Race() }()
}
