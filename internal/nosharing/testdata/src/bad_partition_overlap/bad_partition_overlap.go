package bad_partition_overlap

func Run() {
	buf := make([]int, 2)
	go func() { // want `shared memory .* written without channel transfer and no proven lock/atomic/partition guard`
		buf[0] = 1
	}()
	go func() { // want `shared memory .* written without channel transfer and no proven lock/atomic/partition guard`
		buf[0] = 2
	}()
}
