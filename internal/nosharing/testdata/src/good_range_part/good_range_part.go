package good_range_part

import (
	"sync"

	"padlib"
)

func Run() {
	src := make([]byte, 4)
	dst := make([]byte, 4)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s := dst[0:2]
		s[0] = 1
		s[1] = 2
		padlib.Pad(src[0:2], s)
	}()
	go func() {
		defer wg.Done()
		s := dst[2:4]
		s[0] = 3
		s[1] = 4
		padlib.Pad(src[2:4], s)
	}()
	wg.Wait()
}
