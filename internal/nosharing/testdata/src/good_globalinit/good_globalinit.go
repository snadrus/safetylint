package good_globalinit

var cfg map[string]int

func init() {
	cfg = map[string]int{"a": 1}
}

func Run() int {
	ch := make(chan int, 1)
	go func() {
		ch <- cfg["a"]
	}()
	return <-ch
}
