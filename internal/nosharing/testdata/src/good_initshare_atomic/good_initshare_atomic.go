package good_initshare_atomic

import "sync/atomic"

type Obj struct {
	n  atomic.Int64
	ok atomic.Bool
}

func (o *Obj) loop() {
	o.n.Add(1)
	_ = o.ok.Load()
	o.ok.Store(true)
}

func New() *Obj {
	o := &Obj{}
	o.n.Store(1)
	o.ok.Store(false)
	go o.loop()
	return o
}
