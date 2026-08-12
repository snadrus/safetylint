package cgo // want `import "C" \(cgo/extern C\) escapes Go memory safety`

/*
static int fortytwo(void) { return 42; }
*/
import "C"

func FortyTwo() int {
	return int(C.fortytwo())
}
