package bad_global_nestedmu

import "sync"

// Nested mutex exists but is not held at the write: freeze must still refuse.
type diff struct {
	cdmx   sync.Mutex
	staged map[int]int
}

type holder struct {
	diff
}

var reg = holder{diff: diff{staged: map[int]int{}}}

func Stage(k, v int) {
	reg.staged[k] = v // want `write to package global reg`
}

func Run() {
	go Stage(1, 2)
	Stage(3, 4)
}
