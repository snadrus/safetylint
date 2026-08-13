package good_wg_localmu

import "sync"

func Fanout() error {
	var mu sync.Mutex
	var err error
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			mu.Lock()
			err = nil
			mu.Unlock()
		}()
	}
	wg.Wait()
	return err
}

func Run() {
	go func() { _ = Fanout() }()
	_ = Fanout()
}
