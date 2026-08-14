package good_curio_stride

import "sync"

func pad(in, out []byte) {
	copy(out, in)
}

func Run(n int) {
	unpadded := make([]byte, n*127)
	padded := make([]byte, n*128)
	var wg sync.WaitGroup
	concurrency := 4
	chunkPerWorker := (n + concurrency - 1) / concurrency
	for w := range concurrency {
		start := w * chunkPerWorker
		end := min((w+1)*chunkPerWorker, n)
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			for i := start; i < end; i++ {
				in := unpadded[i*127 : (i+1)*127]
				out := padded[i*128 : (i+1)*128]
				pad(in, out)
			}
		}(start, end)
	}
	wg.Wait()
}
