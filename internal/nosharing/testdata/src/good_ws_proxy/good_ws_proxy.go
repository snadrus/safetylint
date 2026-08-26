package good_ws_proxy

type Conn struct{ n int }

func (c *Conn) ReadMessage() (int, []byte, error) {
	c.n++
	return 0, nil, nil
}
func (c *Conn) WriteMessage(int, []byte) error {
	c.n++
	return nil
}

func proxyCopy(dst, src *Conn, errc chan<- error) {
	for {
		_, p, err := src.ReadMessage()
		if err != nil {
			errc <- err
			return
		}
		if err := dst.WriteMessage(0, p); err != nil {
			errc <- err
			return
		}
	}
}

func Run() {
	a, b := &Conn{}, &Conn{}
	errc := make(chan error, 2)
	go proxyCopy(a, b, errc)
	go proxyCopy(b, a, errc)
}
