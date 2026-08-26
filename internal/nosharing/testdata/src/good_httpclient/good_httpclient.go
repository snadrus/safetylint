package good_httpclient

import (
	"context"
	"net/http"
)

func Poll(ctx context.Context, c *http.Client) { // want Poll:"mayShareParams param0:read param1:read"
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1/", nil)
		_, _ = c.Do(req)
	}()
}

func Run() {
	c := &http.Client{}
	go func() {
		_, _ = c.Get("http://127.0.0.1/")
	}()
}
