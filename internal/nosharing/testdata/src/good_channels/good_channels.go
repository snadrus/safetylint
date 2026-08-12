package good_channels

func WorkerPool() int {
	jobs := make(chan int, 3)
	results := make(chan int, 3)
	for i := 0; i < 2; i++ {
		go func() {
			for j := range jobs {
				results <- j * 2
			}
		}()
	}
	for i := 1; i <= 3; i++ {
		jobs <- i
	}
	close(jobs)
	sum := 0
	for i := 0; i < 3; i++ {
		sum += <-results
	}
	return sum
}
