package good_curio_locks

import "sync"

type slot struct {
	r    [2]int
	refs int
}

func (s *slot) unlock() {
	s.r[0]--
}

type Index struct {
	lk    sync.Mutex
	locks map[int]*slot
}

func (i *Index) lockWith(k int) {
	i.lk.Lock()
	slk := i.locks[k]
	if slk == nil {
		slk = &slot{}
		i.locks[k] = slk
	}
	slk.refs++
	i.lk.Unlock()

	go func() {
		i.lk.Lock()
		slk.unlock()
		slk.refs--
		if slk.refs == 0 {
			delete(i.locks, k)
		}
		i.lk.Unlock()
	}()
}

func Run() {
	i := &Index{locks: map[int]*slot{}}
	i.lockWith(1)
}
