package good_share_replaced

// EnableChangeDetection shape: the param is stored into the root, then the
// field is definitely replaced with a private copy before the goroutine
// starts — the callee does not retain the caller's object.
type root struct {
	tree map[string]int
}

func clone(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func Enable(obj map[string]int) {
	r := &root{tree: obj}
	r.tree = clone(obj)
	go r.monitor()
}

func (r *root) monitor() {
	for i := 0; i < 2; i++ {
		r.tree["k"] = i
	}
}

func Run() {
	cfg := map[string]int{"k": 0}
	Enable(cfg)
	cfg["k"] = 1 // not shared: Enable replaced its copy before spawning
}
