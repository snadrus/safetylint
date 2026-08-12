package sharero

// Start retains p for concurrent reads after return.
func Start(p *int) { // want Start:"mayShareParams param0:read"
	go func() {
		_ = *p
	}()
}
