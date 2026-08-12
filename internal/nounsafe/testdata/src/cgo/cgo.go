package cgo // want `import "C" \(cgo/extern C\) is not verified. Check its safety and the adapter's safety yourself`

/*
static int fortytwo(void) { return 42; }
*/
import "C"

func FortyTwo() int {
	return int(C.fortytwo())
}
