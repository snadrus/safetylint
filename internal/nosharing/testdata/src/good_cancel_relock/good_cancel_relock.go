package good_cancel_relock

import (
	"context"
	"sync"
)

type sectorLock struct {
	cond *sync.Cond
	r    [2]uint
	w    int
	refs uint
}

func (l *sectorLock) unlock() {
	l.cond.L.Lock()
	defer l.cond.L.Unlock()
	l.r[0]--
	l.w = 0
	l.cond.Broadcast()
}

type indexLocks struct {
	lk    sync.Mutex
	locks map[int]*sectorLock
}

func (i *indexLocks) lockWith(ctx context.Context, sector int) {
	i.lk.Lock()
	slk, ok := i.locks[sector]
	if !ok {
		slk = &sectorLock{cond: sync.NewCond(&sync.Mutex{})}
		i.locks[sector] = slk
	}
	slk.refs++
	i.lk.Unlock()

	go func() {
		<-ctx.Done()
		i.lk.Lock()
		slk.unlock()
		slk.refs--
		if slk.refs == 0 {
			delete(i.locks, sector)
		}
		i.lk.Unlock()
	}()
}

func Run() {
	i := &indexLocks{locks: map[int]*sectorLock{}}
	ctx, cancel := context.WithCancel(context.Background())
	i.lockWith(ctx, 1)
	cancel()
}
