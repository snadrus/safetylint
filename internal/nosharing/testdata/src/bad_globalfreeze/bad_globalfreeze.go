package main

var counter int

func main() {
	go work()
	counter++ // want `write to package global counter`
	bump()
}

func work() {
	_ = counter
}

func bump() {
	counter++ // want `write to package global counter`
}
