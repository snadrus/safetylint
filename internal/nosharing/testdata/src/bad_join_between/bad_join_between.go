package bad_join_between

import "sync"

// A spawner write between go and Wait races the worker: neither the
// before-go nor the after-join filter may hide it.
func Run() {
	buf := make([]byte, 8)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // want `shared memory`
		defer wg.Done()
		buf[0] = 1
	}()
	buf[0] = 2
	wg.Wait()
	_ = buf[0]
}
