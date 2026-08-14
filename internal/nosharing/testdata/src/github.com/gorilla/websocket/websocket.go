// Stub for analysistest GOPATH mode (real module used outside testdata).
package websocket

type Conn struct {
	n int
}

func (c *Conn) ReadMessage() (int, []byte, error) {
	c.n++
	return 0, nil, nil
}

func (c *Conn) NextReader() (int, interface{}, error) {
	c.n++
	return 0, nil, nil
}

func (c *Conn) SetReadDeadline() {
	c.n++
}

func (c *Conn) WriteMessage(int, []byte) error {
	c.n++
	return nil
}

func (c *Conn) NextWriter(int) (interface{}, error) {
	c.n++
	return nil, nil
}

func (c *Conn) SetWriteDeadline() {
	c.n++
}

func (c *Conn) Close() error {
	return nil
}
