package good_bufpool

func Run() {
	throttle := make(chan int, 2)
	throttle <- 0
	throttle <- 1
	bufs := make([][8]byte, 2)
	idx := <-throttle
	go func() {
		bufs[idx][0] = 1
		throttle <- idx
	}()
}

func SliceToken() {
	throttle := make(chan int, 2)
	throttle <- 0
	throttle <- 1
	tbufs := make([][8]byte, 2)
	idx := <-throttle
	go func() {
		_ = tbufs[idx][:]
		tbufs[idx][0] = 1
		throttle <- idx
	}()
}
