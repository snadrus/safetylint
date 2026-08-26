package good_wg_readonly

import (
	"sync"

	"parselib"
)

type Status struct {
	FilAddress string
	Balance    int
}

func Fanout() Status {
	status := Status{FilAddress: "f1"}
	var (
		wg     sync.WaitGroup
		exists bool
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		if status.FilAddress == "" {
			return
		}
		_ = parselib.Parse(status.FilAddress)
		exists = true
	}()
	go func() {
		defer wg.Done()
		_ = parselib.Parse(status.FilAddress)
	}()
	wg.Wait()
	if exists {
		status.Balance = 1
	}
	return status
}

func Run() {
	go func() { _ = Fanout() }()
	_ = Fanout()
}
