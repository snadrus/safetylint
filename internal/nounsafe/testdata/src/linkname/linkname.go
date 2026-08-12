package linkname

import _ "fmt"

//go:linkname foo runtime.foo // want `//go:linkname escapes Go memory safety`
func foo() {}
