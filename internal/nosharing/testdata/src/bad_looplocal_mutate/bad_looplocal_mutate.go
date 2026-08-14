package bad_looplocal_mutate

type cell struct{ n int }

func Run() {
	for i := 0; i < 3; i++ {
		c := cell{n: i}
		go func() { // want `shared memory`
			_ = c.n
		}()
		c.n = 9 // same-iteration field store after go
	}
}
