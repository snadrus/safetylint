package good_stride

func Run(n int) {
	buf := make([]int, n)
	k := (n + 3) / 4
	for w := 0; w < 4; w++ {
		start := w * k
		end := min((w+1)*k, n)
		go func(start, end int) {
			for i := start; i < end; i++ {
				buf[i] = i
			}
		}(start, end)
	}
}
