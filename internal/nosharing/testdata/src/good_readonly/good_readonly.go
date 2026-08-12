package good_readonly

type Config struct {
	Name string
	N    int
}

func Run(cfg Config) int { // want Run:"mayShareParams param0:read"
	ch := make(chan int, 1)
	go func() {
		// cfg is captured read-only (value copy of a pointer-free struct).
		ch <- len(cfg.Name) + cfg.N
	}()
	return <-ch
}

func RunPtr(cfg *Config) int { // want RunPtr:"mayShareParams param0:read"
	ch := make(chan int, 1)
	go func() {
		// cfg pointer captured but never written through.
		ch <- len(cfg.Name) + cfg.N
	}()
	return <-ch
}
