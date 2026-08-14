package good_treed_pool

import (
	"io"
	"sync"

	"padlib"
)

// Curio treed_build worker shape: buffers pop from a mutex-guarded pool,
// the parent fills the leased buffer before spawning (ReadFull + header
// trim), the worker pads into a fresh buffer and rebinds the header, hashes
// in a same-package helper, persists via WriteAt (payload read), copies the
// apex into a stride slot, and returns the buffer under the same mutex.
func hashChunk(data [][]byte) {
	l1 := len(data[0]) / 64
	for i := 0; i < l1; i++ {
		copy(data[1][i*32:(i+1)*32], data[0][i*64:(i+1)*64])
		data[1][i*32+31] &= 0x3f
	}
}

func Build(r io.Reader, out io.WriterAt, unpadded bool, n int) error { // want Build:"mayShareParams param1:read param2:read"
	var bufLk sync.Mutex
	workerBuffers := [][][]byte{
		{make([]byte, 128), make([]byte, 64)},
		{make([]byte, 128), make([]byte, 64)},
	}
	apex := make([]byte, n*32)

	var wg sync.WaitGroup
	var errLk sync.Mutex
	var oerr error

	for processed := 0; processed < n; processed++ {
		bufLk.Lock()
		if len(workerBuffers) == 0 {
			bufLk.Unlock()
			continue
		}
		workBuffer := workerBuffers[len(workerBuffers)-1]
		workerBuffers = workerBuffers[:len(workerBuffers)-1]
		bufLk.Unlock()

		errLk.Lock()
		if oerr != nil {
			errLk.Unlock()
			return oerr
		}
		errLk.Unlock()

		if unpadded {
			workBuffer[0] = workBuffer[0][:127]
		}
		if _, err := io.ReadFull(r, workBuffer[0]); err != nil {
			return err
		}

		wg.Add(1)
		go func(startOffset int) {
			defer wg.Done()

			if unpadded {
				padded := make([]byte, 128)
				padlib.Pad(workBuffer[0], padded)
				workBuffer[0] = padded
			}
			hashChunk(workBuffer)

			apexHash := workBuffer[len(workBuffer)-1]
			_ = apexHash[startOffset%len(apexHash)]
			_ = apex

			for layer, layerData := range workBuffer {
				if _, werr := out.WriteAt(layerData, int64(layer)); werr != nil {
					errLk.Lock()
					oerr = werr
					errLk.Unlock()
					return
				}
			}

			bufLk.Lock()
			workerBuffers = append(workerBuffers, workBuffer)
			bufLk.Unlock()
		}(processed)
	}
	wg.Wait()
	return oerr
}
