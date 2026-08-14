package bad_looplocal_hoist

type cell struct{ n int }

func Run() {
	c := cell{}
	for i := 0; i < 3; i++ {
		go func() { // want `shared memory`
			_ = c.n
		}()
		c.n = i // hoisted cell mutated across iterations
	}
}
