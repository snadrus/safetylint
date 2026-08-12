package bad_init_xpkg_readhot

import "hotlib"

func init() {
	go func() {
		_ = hotlib.St.N // want `init goroutine accesses foreign hot global`
	}()
}
