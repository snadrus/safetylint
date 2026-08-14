package good_curio_locks

import "sync"

// Curio index_locks shape: ctxCond.L is sync.Locker, stored only as
// *sync.Mutex via newCtxCond. Slot r/w under that Locker; refs under Index.lk.
type ctxCond struct {
	L sync.Locker
}

func newCtxCond(l sync.Locker) *ctxCond {
	return &ctxCond{L: l}
}

type slot struct {
	cond *ctxCond
	r    [2]int
	refs int
}

func (s *slot) canLock() bool {
	return s.r[0] == 0
}

func (s *slot) tryLock() bool {
	if !s.canLock() {
		return false
	}
	s.r[0]++
	return true
}

func (s *slot) lock() {
	s.cond.L.Lock()
	defer s.cond.L.Unlock()
	for !s.tryLock() {
	}
}

func (s *slot) unlock() {
	s.cond.L.Lock()
	defer s.cond.L.Unlock()
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
		slk = &slot{cond: newCtxCond(&sync.Mutex{})}
		i.locks[k] = slk
	}
	slk.refs++
	i.lk.Unlock()

	slk.lock()

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
