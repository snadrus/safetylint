package good_localreg

// Same-package registry: the package both freezes (it spawns) and writes
// Registry from an InitOnly helper. SAFETYLINT_NO_FACTS must still allow
// the write — discovery is local, not a Fact import.

var Registry = map[string]int{}

func Reg(name string, v int) bool {
	Registry[name] = v
	return true
}

var _ = Reg("a", 1)

func Boot() {
	go func() {
		_ = Registry["a"]
	}()
}
