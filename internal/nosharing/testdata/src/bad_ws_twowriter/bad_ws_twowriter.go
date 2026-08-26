package bad_ws_twowriter

type Conn struct{ n int }

func (c *Conn) ReadMessage() (int, []byte, error) { return 0, nil, nil }
func (c *Conn) WriteMessage(int, []byte) error {
	c.n++
	return nil
}

func writeOnly(c *Conn) {
	_ = c.WriteMessage(0, []byte("x"))
}

func Run() {
	a := &Conn{}
	go writeOnly(a) // want `shared memory .* written without channel transfer`
	go writeOnly(a) // want `shared memory .* written without channel transfer`
}
