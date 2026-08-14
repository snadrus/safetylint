package bad_ws_tworeaders

import "github.com/gorilla/websocket"

func Run(c *websocket.Conn) { // want Run:"mayShareParams param0:write"
	go func() { // want `shared memory`
		_, _, _ = c.ReadMessage()
	}()
	go func() { // want `shared memory`
		_, _, _ = c.ReadMessage()
	}()
}
