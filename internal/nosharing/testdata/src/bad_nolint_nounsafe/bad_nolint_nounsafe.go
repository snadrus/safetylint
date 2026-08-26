package bad_nolint_nounsafe

func Race() {
	counter := 0
	//nolint:nounsafe
	go func() { // want `shared memory .* written without channel transfer`
		counter++
	}()
	counter++
}
