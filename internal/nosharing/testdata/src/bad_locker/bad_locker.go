package bad_locker

import "sync"

// Custom Locker is not *sync.Mutex — Lock/Unlock must not count as a mutex.
type fakeLocker struct{ n int }

func (f *fakeLocker) Lock()   { f.n++ }
func (f *fakeLocker) Unlock() { f.n-- }

type box struct {
	L    sync.Locker
	refs int
}

func (b *box) unlock() {
	b.L.Lock()
	b.refs--
	b.L.Unlock()
}

func Run() {
	b := &box{L: &fakeLocker{}}
	go func() { // want `shared memory` `shared memory`
		b.unlock()
	}()
	b.unlock()
}
