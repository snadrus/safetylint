package good_freeze

type Data struct {
	N int
}

func Pipeline() int {
	ch := make(chan *Data, 1)
	d := &Data{N: 1}
	// Initialize before send, then freeze.
	ch <- d
	got := <-ch
	return got.N // read-only after receive: OK
}
