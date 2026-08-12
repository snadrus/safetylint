package bad_freeze_send

type Data struct {
	N int
}

func Pipeline() {
	ch := make(chan *Data, 1)
	d := &Data{N: 1}
	ch <- d // want `channel send of pointer-carrying value: memory is frozen after send but a write was found`
	d.N = 2
	<-ch
}
