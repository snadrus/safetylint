package bad_curio_locks

import "sync"

type slot struct {
	refs int
}

func (s *slot) unlock() {
	s.refs--
}

type Index struct { // want Index:"concurrentSafe"
	lk    sync.Mutex
	locks map[int]*slot
}

func (i *Index) lockWith(k int) {
	i.lk.Lock()
	slk := i.locks[k]
	i.lk.Unlock()
	go func() { // want `shared memory` `shared memory`
		slk.unlock() // no parent mutex
	}()
}

func Run() {
	i := &Index{locks: map[int]*slot{1: {}}}
	i.lockWith(1)
}
