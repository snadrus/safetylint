package bad_afterfunc_callback

import (
	"sync"
	"time"
)

type engine struct {
	mu sync.Mutex
	n  int
}

func (e *engine) arm() {
	e.mu.Lock()
	defer e.mu.Unlock()
	time.AfterFunc(time.Millisecond, func() { // want `shared memory .* written without channel transfer`
		e.n++
	})
	e.n++
}
