package bad_initreg

import "initreg"

func Later() {
	initreg.Reg("x", 1) // want `InitOnly function Reg called outside init/var initializer`
}
