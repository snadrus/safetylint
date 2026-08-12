package bad_once_unsync

import "sync"

type lazy struct {
	once  sync.Once
	value int
}

var slot lazy

func load() {
	slot.once.Do(func() {
		slot.value = 1 // want `write to package global slot`
	})
}

func Run() {
	// Unsynchronized read concurrent with Once writer — Once must not
	// exempt the write when any access skips Do.
	go func() { _ = slot.value }()
	load()
}
