package good_curio_locks

import "sync"

// Curio index_locks shape: slot r/w under the slot mutex (canLock←tryLock
// inherit); refs under Index.lk held across unlock().
type slot struct {
	mu   sync.Mutex
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
	s.mu.Lock()
	defer s.mu.Unlock()
	for !s.tryLock() {
	}
}

func (s *slot) unlock() {
	s.mu.Lock()
	defer s.mu.Unlock()
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
