package bad_iface_arg

import "sync"

type T struct {
	mu sync.Mutex
	n  int
}

func poke(t *T) { t.n++ }

func Run() {
	t := &T{}
	go poke(t) // want `shared memory .* written without channel transfer`
}
