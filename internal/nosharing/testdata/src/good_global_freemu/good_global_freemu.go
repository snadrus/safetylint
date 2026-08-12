package good_global_freemu

import "sync"

var (
	priceMu     sync.Mutex
	priceCached float64
)

func bump() {
	priceMu.Lock()
	defer priceMu.Unlock()
	priceCached++
}

func Run() {
	go bump()
	bump()
}
