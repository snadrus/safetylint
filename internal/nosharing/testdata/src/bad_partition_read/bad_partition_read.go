package bad_partition_read

func Run() int {
	buf := make([]int, 2)
	go func() { // want `shared memory .* written without channel transfer and no proven lock/atomic/partition guard`
		buf[0] = 1
	}()
	return buf[0]
}
