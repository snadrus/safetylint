package linkname

import _ "fmt"

//go:linkname foo runtime.foo // want `//go:linkname is not verified. Check its safety and the adapter's safety yourself`
func foo() {}
