package good_wg_join

import "sync"

func Fanout() (int, int, error) {
	var a, b int
	var errA, errB error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		a = 1
		errA = nil
	}()
	go func() {
		defer wg.Done()
		b = 2
		errB = nil
	}()
	wg.Wait()
	if errA != nil {
		return 0, 0, errA
	}
	return a, b, errB
}

func Run() {
	go func() { _, _, _ = Fanout() }()
	_, _, _ = Fanout()
}
