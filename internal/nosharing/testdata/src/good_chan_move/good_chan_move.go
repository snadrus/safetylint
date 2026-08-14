package good_chan_move

import (
	"fmt"
	"io"
)

// asyncwrite.BackgroundWriter shape: the sender copies into a fresh buffer
// and relinquishes it on send; the receiving worker only reads it into the
// destination writer.
type bw struct {
	dest io.Writer
	ch   chan []byte
	done chan error
}

func New(dest io.Writer) *bw { // want New:"mayShareParams param0:read"
	w := &bw{dest: dest, ch: make(chan []byte, 4), done: make(chan error, 1)}
	go w.worker()
	return w
}

func (w *bw) worker() {
	var err error
	for data := range w.ch {
		if _, werr := w.dest.Write(data); werr != nil {
			err = werr
			break
		}
	}
	w.done <- err
}

func (w *bw) Write(p []byte) (int, error) {
	b := make([]byte, len(p))
	copy(b, p)
	select {
	case w.ch <- b:
		return len(b), nil
	case err := <-w.done:
		return 0, err
	}
}

func (w *bw) Finish() error {
	if w.ch == nil {
		return nil
	}
	close(w.ch)
	w.ch = nil
	if err := <-w.done; err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}
