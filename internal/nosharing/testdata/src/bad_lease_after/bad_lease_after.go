package bad_lease_after

func Run() {
	tokens := make(chan int, 1)
	tokens <- 0
	buf := make([]int, 1)
	go func() { // want `shared memory`
		idx := <-tokens
		tokens <- idx
		buf[idx]++ // use after send-back
	}()
	go func() { // want `shared memory`
		idx := <-tokens
		buf[idx]++
		tokens <- idx
	}()
}
