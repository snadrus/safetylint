package good_httpserver

import (
	"context"
	"errors"
	"net/http"
)

func StartAdmin(ctx context.Context, srv *http.Server) { // want StartAdmin:"mayShareParams param0:read param1:read"
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			panic(err)
		}
	}()
}

// Listen re-exports GOROOT retain of *Server (curatedRetains).
func Listen(s *http.Server) error { // want Listen:"mayShareParams param0:write"
	return s.ListenAndServe()
}

func serveAndStop() {
	srv := &http.Server{Addr: ":0"}
	go srv.ListenAndServe()
	_ = srv.Shutdown(context.Background())
}
