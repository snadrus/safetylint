package bad_initshare_write

type Obj struct {
	n int
}

func (o *Obj) loop() {
	o.n++
}

func New() *Obj {
	o := &Obj{n: 1}
	go o.loop() // want `shared memory .* written without channel transfer and no proven lock/atomic/partition guard`
	return o
}

func (o *Obj) bump() {
	o.n++
}

func (o *Obj) loopMethod() {
	o.bump()
}

func BadMethod() {
	o := &Obj{n: 1}
	go o.loopMethod() // want `shared memory .* written without channel transfer and no proven lock/atomic/partition guard`
}
