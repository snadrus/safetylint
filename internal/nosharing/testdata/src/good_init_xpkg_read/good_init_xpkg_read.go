package good_init_xpkg_read

import "plainlib"

func init() {
	go func() {
		// Frozen foreign global: never written after init in plainlib.
		_ = plainlib.X
	}()
}
