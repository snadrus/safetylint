package bad_xpkg_wrap

import "sharewrap"

func Run() {
	x := 0
	sharewrap.Wrap(&x) // want `shared memory from Fact-bearing call written`
	x++
}
