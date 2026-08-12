package bad_ptrarg

func worker(p *int) {
	*p = 1
}

func Main() {
	x := 0
	go worker(&x) // want `shared memory .* written without channel transfer`
	x = 2
}
