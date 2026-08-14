package good_xpkg_stride

import (
	"sync"

	"padlib"
)

// Curio index_helpers shape: stride workers call a cross-package (bodyless
// at analysis time) pad helper whose WritesParams Fact says only the dest
// slice is written.
func Run(n int) {
	unpadded := make([]byte, n*127)
	padded := make([]byte, n*128)
	var wg sync.WaitGroup
	concurrency := 4
	chunkPerWorker := (n + concurrency - 1) / concurrency
	for w := 0; w < concurrency; w++ {
		start := w * chunkPerWorker
		end := min((w+1)*chunkPerWorker, n)
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			for i := start; i < end; i++ {
				in := unpadded[i*127 : (i+1)*127]
				out := padded[i*128 : (i+1)*128]
				padlib.Pad(in, out)
			}
		}(start, end)
	}
	wg.Wait()
}
