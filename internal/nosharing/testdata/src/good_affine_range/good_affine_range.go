package good_affine_range

import "sync"

const K = 2

func Run() {
	n := 4
	dst := make([]byte, n)
	var wg sync.WaitGroup
	c := 2
	for w := 0; w < 2; w++ {
		start := w * c
		end := min((w+1)*c, n)
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			for i := start; i < end; i++ {
				s := dst[i*K : (i+1)*K]
				s[0] = 1
				s[1] = 2
			}
		}(start, end)
	}
	wg.Wait()
}
