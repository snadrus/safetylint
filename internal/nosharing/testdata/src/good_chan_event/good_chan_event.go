package good_chan_event

type Data struct{ N int }

type Ev struct {
	Src   int
	Tasks map[string][]int
	D     *Data
}

func Run() int {
	ch := make(chan Ev, 1)
	m := map[string][]int{"a": {1}}
	d := &Data{N: 1}
	ch <- Ev{Src: 1, Tasks: m, D: d}
	go func() {
		e := <-ch
		n := 0
		for range e.Tasks {
			n++
		}
		_ = e.Src + n + e.D.N
	}()
	return 0
}
