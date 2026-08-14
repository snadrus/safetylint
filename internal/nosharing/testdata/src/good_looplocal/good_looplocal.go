package good_looplocal

func Run() {
	for i := 0; i < 3; i++ {
		n := i
		go func() {
			_ = n
		}()
	}
}
