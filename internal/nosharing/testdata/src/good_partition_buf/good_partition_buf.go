package good_partition_buf

func Run() {
	buf := make([]int, 2)
	go func() {
		buf[0] = 1
	}()
	go func() {
		buf[1] = 2
	}()
}
