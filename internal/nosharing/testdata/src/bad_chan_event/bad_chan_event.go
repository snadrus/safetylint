package bad_chan_event

type Data struct{ N int }

type Ev struct {
	Src   int
	Tasks map[string][]int
	D     *Data
}

func SendMutate() {
	ch := make(chan Ev, 1)
	d := &Data{N: 1}
	m := map[string][]int{"a": {1}}
	ch <- Ev{Src: 1, Tasks: m, D: d} // want `channel send of pointer-carrying value: memory is frozen after send but a write was found`
	d.N = 2
	m["x"] = []int{2}
	<-ch
}

func RecvWrite(ch <-chan Ev) {
	e := <-ch // want `channel receive of pointer-carrying value: memory is frozen after send but receiver writes through it`
	e.D.N = 9
	e.Tasks["x"] = []int{9}
}
