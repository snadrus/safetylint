package initreg

// Package that spawns (so globals freeze) but exports an InitOnly registrar.

var Registry = map[string]int{}

func init() {
	go func() {}()
}

// Reg writes Registry and is only meant for init/var initializers.
func Reg(name string, v int) bool { // want Reg:"initOnly"
	Registry[name] = v
	return true
}
