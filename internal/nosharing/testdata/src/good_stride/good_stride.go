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

func RangePad(n int) {
	buf := make([]byte, n*2)
	k := (n + 3) / 4
	for w := range 4 {
		start := w * k
		end := min((w+1)*k, n)
		go func(start, end int) {
			for i := start; i < end; i++ {
				out := buf[i*2 : (i+1)*2]
				out[0] = 1
			}
		}(start, end)
	}
}
