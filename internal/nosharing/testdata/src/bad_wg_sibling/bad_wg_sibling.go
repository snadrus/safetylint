package bad_wg_sibling

import "sync"

// Sibling goroutine reads a while another writes it under the same WaitGroup.
// Wait must not make this look exclusive.
func Race() int {
	var a int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // want `shared memory`
		defer wg.Done()
		a = 1
	}()
	go func() { // want `shared memory`
		_ = a
	}()
	wg.Wait()
	return a
}

func Run() {
	go func() { _ = Race() }()
}
