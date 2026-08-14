package good_curio_lease

import "sync"

func hashChunk(data [][]byte) {
	if len(data) > 0 && len(data[0]) > 0 {
		data[0][0] = 1
	}
}

func Run() {
	var bufLk sync.Mutex
	var workWg sync.WaitGroup
	workerBuffers := [][][]byte{{make([]byte, 8)}, {make([]byte, 8)}}
	for processed := 0; processed < 2; processed++ {
		bufLk.Lock()
		workBuffer := workerBuffers[len(workerBuffers)-1]
		workerBuffers = workerBuffers[:len(workerBuffers)-1]
		bufLk.Unlock()
		workBuffer[0][0] = 2
		workWg.Add(1)
		go func() {
			defer workWg.Done()
			hashChunk(workBuffer)
			bufLk.Lock()
			workerBuffers = append(workerBuffers, workBuffer)
			bufLk.Unlock()
		}()
	}
	workWg.Wait()
}
