package good_postdom_go

import "time"

func Run(cond bool) {
	var wait time.Duration
	if cond {
		wait = time.Millisecond
	}
	go func() {
		time.Sleep(wait)
	}()
}
