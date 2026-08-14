package bad_stride_overlap

func Run(n int) { // want Run:"mayShareParams param0:read"
	buf := make([]int, n)
	go func() { // want `shared memory .* written without channel transfer`
		for i := 0; i < n; i++ {
			buf[i] = 1
		}
	}()
	go func() { // want `shared memory .* written without channel transfer`
		for i := 0; i < n; i++ {
			buf[i] = 2
		}
	}()
}
