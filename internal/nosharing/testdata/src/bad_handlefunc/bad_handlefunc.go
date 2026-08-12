package bad_handlefunc

import "net/http"

func Run() {
	n := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { // want `shared memory from Fact-bearing call written`
		n++
	})
	_ = mux
	n++
}
