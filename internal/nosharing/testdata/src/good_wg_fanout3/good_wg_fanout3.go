package good_wg_fanout3

import "sync"

func Fanout() (int, int, int) {
	var a, b, c int
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		a = 1
	}()
	go func() {
		defer wg.Done()
		b = 2
	}()
	go func() {
		defer wg.Done()
		c = 3
	}()
	wg.Wait()
	return a, b, c
}

func Run() {
	go func() { _, _, _ = Fanout() }()
}
