package good_nontied_mu

import "sync"

type slot struct {
	refs int
}

type Index struct {
	lk sync.Mutex
	m  map[int]*slot
}

func Run() {
	i := &Index{m: map[int]*slot{1: {}}}
	i.lk.Lock()
	slk := i.m[1]
	i.lk.Unlock()
	go func() {
		i.lk.Lock()
		slk.refs++
		i.lk.Unlock()
	}()
	i.lk.Lock()
	slk.refs++
	i.lk.Unlock()
}
