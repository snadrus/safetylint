package good_valuechan

func Values() int {
	ch := make(chan int, 1)
	x := 1
	ch <- x
	x = 2 // pointer-free value: post-send mutation of local is fine
	go func() {
		ch <- 3
	}()
	return x + <-ch + <-ch
}
