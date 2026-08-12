package good_handle_register

import "net/http"

var ready int

func init() {
	// Registration in init must not freeze the package.
	http.HandleFunc("/", func(http.ResponseWriter, *http.Request) {})
}

func main() {
	ready = 1
	go func() {
		_ = http.ListenAndServe(":0", nil)
	}()
}
