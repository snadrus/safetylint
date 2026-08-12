package main

var config map[string]int

func main() {
	config = map[string]int{"n": 3}
	config["m"] = 4
	setup()
	done := make(chan int)
	go func() {
		done <- config["n"]
	}()
	<-done
}

// setup is called only from main before the first spawn, so its global
// writes are still in the single-threaded initialization phase.
func setup() {
	config["k"] = 5
}
