package bad_field_mutex

import "sync"

type box struct {
	mu1, mu2 sync.Mutex
	n        int
}

var g box

func bump1() {
	g.mu1.Lock()
	g.n++ // want `write to package global g`
	g.mu1.Unlock()
}

func bump2() {
	g.mu2.Lock()
	g.n++ // want `write to package global g`
	g.mu2.Unlock()
}

func Run() {
	go bump1()
	go bump2()
	bump1()
}
