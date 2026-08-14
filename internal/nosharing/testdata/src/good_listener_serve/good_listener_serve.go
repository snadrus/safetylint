package good_listener_serve

import (
	"net"
	"net/http"
)

// pdptool shape: Serve on a listener in a goroutine; the deferred Close from
// the parent is the documented way to stop it (net.Listener anchor).
func Run() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: http.NewServeMux()}

	go func() {
		_ = server.Serve(ln)
	}()

	defer func() {
		_ = server.Close()
		_ = ln.Close()
	}()
	return nil
}
