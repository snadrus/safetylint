package good_initreg

import "initreg"

var _ = initreg.Reg("a", 1)

func init() {
	initreg.Reg("b", 2)
}
