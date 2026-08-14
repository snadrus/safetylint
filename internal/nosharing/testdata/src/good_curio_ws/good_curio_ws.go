package good_curio_ws

import "github.com/gorilla/websocket"

func Run(client, backend *websocket.Conn) { // want Run:"mayShareParams param0:write param1:write"
	errc := make(chan error, 2)
	go proxyCopy(client, backend, errc)
	go proxyCopy(backend, client, errc)
	<-errc
}

func proxyCopy(dst, src *websocket.Conn, errc chan<- error) {
	_, p, err := src.ReadMessage()
	if err != nil {
		errc <- err
		return
	}
	if err := dst.WriteMessage(1, p); err != nil {
		errc <- err
	}
}
