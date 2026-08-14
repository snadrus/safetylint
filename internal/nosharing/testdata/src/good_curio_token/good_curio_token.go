package good_curio_token

type Writer struct {
	buf      [8]byte
	tbufs    [][8]byte
	throttle chan int
	n        int
}

func (w *Writer) Write() { // want Write:"mayShareParams recv:read"
	if w.throttle == nil {
		w.throttle = make(chan int, 1)
		w.throttle <- 0
	}
	if w.tbufs == nil {
		w.tbufs = make([][8]byte, 1)
	}
	w.n++
	idx := <-w.throttle
	copy(w.tbufs[idx][:], w.buf[:])
	go func() {
		defer func() { w.throttle <- idx }()
		w.tbufs[idx][0] = 1
	}()
	w.n++
}
