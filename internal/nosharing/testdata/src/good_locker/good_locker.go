package good_locker

import "sync"

// Locker field stored only as *sync.Mutex — Lock/Unlock are real mutex ops.
type box struct {
	L sync.Locker
	n int
}

func newBox() *box {
	return &box{L: &sync.Mutex{}}
}

func (b *box) add() {
	b.L.Lock()
	defer b.L.Unlock()
	b.n++
}

func Run() {
	b := newBox()
	go b.add()
	b.add()
}
