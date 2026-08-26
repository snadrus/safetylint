package bad_handlefunc

import "net/http"

func Run() {
	n := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { // want `shared memory .* written without channel transfer`
		n++
	})
	_ = mux
	n++
}
