package good_once_lazy

import "sync"

type lazy struct {
	once  sync.Once
	value int
	err   error
}

func (l *lazy) get(load func() (int, error)) (int, error) {
	l.once.Do(func() {
		l.value, l.err = load()
	})
	return l.value, l.err
}

var slot lazy

func Value() int {
	v, _ := slot.get(func() (int, error) { return 42, nil })
	return v
}

func Run() {
	go func() { _ = Value() }()
	_ = Value()
}
