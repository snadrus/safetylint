// A program that safetylint accepts: channels only, freeze after send,
// WaitGroup for joining, read-only config capture.
package main

import (
	"fmt"
	"sync"
)

type Config struct {
	Workers int
}

type Result struct {
	N int
}

func main() {
	cfg := &Config{Workers: 2}
	fmt.Println(run(cfg))
}

func run(cfg *Config) int {
	jobs := make(chan int, 3)
	results := make(chan *Result, 3)

	var wg sync.WaitGroup
	for i := 0; i < cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				// Build a result, send it, never mutate after send.
				r := &Result{N: j * 2}
				results <- r
			}
		}()
	}

	for i := 1; i <= 3; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	close(results)

	sum := 0
	for r := range results {
		sum += r.N // read-only through received pointer
	}
	return sum
}
