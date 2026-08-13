package bad_global_freemu_partial

import "sync"

var (
	priceMu     sync.Mutex
	priceCached float64
)

func bumpLocked() {
	priceMu.Lock()
	defer priceMu.Unlock()
	priceCached++ // want `write to package global priceCached after goroutines may have started`
}

func bumpUnlocked() {
	priceCached++ // want `write to package global priceCached after goroutines may have started`
}

func Run() {
	go bumpLocked()
	bumpUnlocked()
}
