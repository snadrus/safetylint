package good_valuecopy

import "sync"

type Addr [20]byte

func lookup(a Addr) int { return int(a[0]) }

func Fanout() (int, int) {
	var a, b int
	var wg sync.WaitGroup
	eth := Addr{1}
	wg.Add(2)
	go func() {
		defer wg.Done()
		a = lookup(eth)
	}()
	go func() {
		defer wg.Done()
		b = lookup(eth)
	}()
	wg.Wait()
	return a, b
}

func Run() {
	go func() { _, _ = Fanout() }()
}
