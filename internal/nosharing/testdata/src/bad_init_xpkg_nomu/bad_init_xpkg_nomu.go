package bad_init_xpkg_nomu

import "hotlib"

func init() {
	go func() {
		hotlib.St.N++ // want `init goroutine accesses foreign hot global`
	}()
}
