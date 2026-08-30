package bad_affine_nowait

import (
	"sync"

	"padlib"
)

const paddedChunk = 4

func Run(fail bool) []byte {
	numChunks := 4
	src := make([]byte, numChunks*3)
	dst := make([]byte, numChunks*paddedChunk)
	var wg sync.WaitGroup
	c := 2
	for w := 0; w < 2; w++ {
		start := w * c
		end := min((w+1)*c, numChunks)
		wg.Add(1)
		go func(start, end int) { // want `shared memory .* written without channel transfer`
			defer wg.Done()
			for i := start; i < end; i++ {
				padlib.Pad(src[i*3:(i+1)*3], dst[i*paddedChunk:(i+1)*paddedChunk])
			}
		}(start, end)
	}
	if fail {
		return dst
	}
	wg.Wait()
	return dst
}
