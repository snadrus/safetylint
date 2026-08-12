package bad_global_httpserve

import "net/http"

var ready int

func main() {
	// HandleFunc is a curated async registration ⇒ freeze spawn point.
	http.HandleFunc("/", func(http.ResponseWriter, *http.Request) {})
	ready = 1 // want `write to package global ready`
}
