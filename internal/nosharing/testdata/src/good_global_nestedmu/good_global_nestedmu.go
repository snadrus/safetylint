package good_global_nestedmu

import (
	"sync"
	"sync/atomic"
)

// Curio deps/config changeNotifier shape: a package global with an embedded
// RWMutex (custom Lock/Unlock wrappers), an atomic mode flag, and an embedded
// value struct whose own Mutex guards sibling maps.
type diff struct {
	cdmx   sync.Mutex
	staged map[int]int
}

type notifier struct {
	sync.RWMutex
	updating int32

	diff

	subs map[int]func()
}

var reg = notifier{
	diff: diff{staged: map[int]int{}},
	subs: map[int]func(){},
}

func (n *notifier) Lock() {
	n.RWMutex.Lock()
	atomic.StoreInt32(&n.updating, 1)
}

func (n *notifier) Unlock() {
	n.cdmx.Lock()
	n.RWMutex.Unlock()
	defer n.cdmx.Unlock()

	atomic.StoreInt32(&n.updating, 0)
	for k := range n.staged {
		if fn := n.subs[k]; fn != nil {
			_ = fn
		}
	}
	n.staged = map[int]int{}
}

func (n *notifier) inform(k, v int) {
	if atomic.LoadInt32(&n.updating) == 0 {
		return
	}
	n.cdmx.Lock()
	defer n.cdmx.Unlock()
	n.staged[k] = v
}

func Subscribe(k int, fn func()) {
	reg.cdmx.Lock()
	defer reg.cdmx.Unlock()
	reg.subs[k] = fn
}

func Set(k, v int) {
	reg.Lock()
	defer reg.Unlock()
	reg.inform(k, v)
}

func Run() {
	go Set(1, 2)
	Set(3, 4)
	Subscribe(5, func() {})
}
