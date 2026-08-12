package bad_capture

func Race() {
	counter := 0
	go func() { // want `shared memory .* written without channel transfer`
		counter++
	}()
	counter++
}
