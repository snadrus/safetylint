package bad_global_httpserve

import "net/http"

var ready int

func main() {
	http.HandleFunc("/", func(http.ResponseWriter, *http.Request) {})
	ready = 1 // still pre-serve
	go func() {
		_ = http.ListenAndServe(":0", nil)
	}()
	ready = 2 // want `write to package global ready`
}
