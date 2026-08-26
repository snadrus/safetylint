package good_stack_pool

import "sync"

func Run() {
	bufLk := sync.Mutex{}
	workerBuffers := [][]byte{make([]byte, 8), make([]byte, 8)}
	bufLk.Lock()
	workBuffer := workerBuffers[len(workerBuffers)-1]
	workerBuffers = workerBuffers[:len(workerBuffers)-1]
	bufLk.Unlock()
	go func() {
		workBuffer[0] = 1
		bufLk.Lock()
		workerBuffers = append(workerBuffers, workBuffer)
		bufLk.Unlock()
	}()
}
