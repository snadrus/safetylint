package good_initshare_chan

type Obj struct {
	ch chan int
}

func (o *Obj) loop() {
	o.ch <- 1
	<-o.ch
}

func New() *Obj {
	o := &Obj{ch: make(chan int, 1)}
	go o.loop()
	return o
}

func Run() {
	o := &Obj{ch: make(chan int, 1)}
	go func() {
		o.ch <- 2
		<-o.ch
	}()
}
