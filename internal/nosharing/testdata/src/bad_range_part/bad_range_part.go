package bad_range_part

import "sync"

func Run() {
	dst := make([]byte, 4)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // want `shared memory .* written without channel transfer`
		defer wg.Done()
		s := dst[0:3]
		s[0] = 1
	}()
	go func() { // want `shared memory .* written without channel transfer`
		defer wg.Done()
		s := dst[2:4]
		s[0] = 2
	}()
	wg.Wait()
}
