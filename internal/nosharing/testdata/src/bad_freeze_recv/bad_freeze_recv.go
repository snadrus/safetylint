package bad_freeze_recv

type Data struct {
	N int
}

func Consumer(ch <-chan *Data) {
	d := <-ch // want `channel receive of pointer-carrying value: memory is frozen after send but receiver writes through it`
	d.N = 99
}
