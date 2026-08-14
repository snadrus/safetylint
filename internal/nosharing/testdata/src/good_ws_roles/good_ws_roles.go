package good_ws_roles

import "github.com/gorilla/websocket"

func Run(c *websocket.Conn) { // want Run:"mayShareParams param0:write"
	go func() {
		_, _, _ = c.ReadMessage()
	}()
	_ = c.WriteMessage(1, nil)
}
