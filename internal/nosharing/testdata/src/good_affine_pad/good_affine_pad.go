package good_affine_pad

import (
	"runtime"
	"sync"

	"padlib"
)

const paddedChunk = 4

func Run() []byte {
	numChunks := 8
	src := make([]byte, numChunks*3)
	dst := make([]byte, numChunks*paddedChunk)
	var wg sync.WaitGroup
	concurrency := runtime.NumCPU()
	if concurrency < 1 {
		concurrency = 1
	}
	chunkPerWorker := (numChunks + concurrency - 1) / concurrency
	for w := 0; w < concurrency; w++ {
		start := w * chunkPerWorker
		end := min((w+1)*chunkPerWorker, numChunks)
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			for i := start; i < end; i++ {
				in := src[i*3 : (i+1)*3]
				out := dst[i*paddedChunk : (i+1)*paddedChunk]
				padlib.Pad(in, out)
			}
		}(start, end)
	}
	wg.Wait()
	sum := 0
	for i := 0; i < len(dst); i++ {
		sum += int(dst[i])
	}
	_ = sum
	return dst
}
