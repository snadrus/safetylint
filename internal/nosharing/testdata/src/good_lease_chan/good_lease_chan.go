package good_lease_chan

func Run() {
	tokens := make(chan int, 2)
	tokens <- 0
	tokens <- 1
	buf := make([]int, 2)
	go func() {
		idx := <-tokens
		buf[idx]++
		tokens <- idx
	}()
	go func() {
		idx := <-tokens
		buf[idx]++
		tokens <- idx
	}()
}
