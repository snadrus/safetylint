package bad_xpkg_readonly

import "sharero"

func Run() {
	x := 0
	sharero.Start(&x) // want `shared memory from Fact-bearing call written`
	x++
}
