package bad_dyngo_export

// Exported helper takes a func and starts it. A same-package call with a known
// body must not complete the callee set — other packages can pass anything.
func RunWith(fn func()) {
	go fn() // want `goroutine with non-static callee`
}

func local() {}

func setup() {
	RunWith(local)
}
