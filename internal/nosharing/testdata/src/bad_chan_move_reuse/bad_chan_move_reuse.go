package bad_chan_move_reuse

// The sender keeps writing into the buffer after sending it: the receiver
// may be reading it concurrently. Freeze-after-send must refuse.
func Run(out chan<- []byte, p []byte) {
	buf := make([]byte, len(p))
	copy(buf, p)
	out <- buf // want `frozen after send`
	buf[0] = 1
}
