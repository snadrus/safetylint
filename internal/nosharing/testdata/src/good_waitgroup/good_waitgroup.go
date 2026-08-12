package good_waitgroup

import "sync"

func Join() int {
	var wg sync.WaitGroup
	ch := make(chan int, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		ch <- 42
	}()
	wg.Wait()
	return <-ch
}
