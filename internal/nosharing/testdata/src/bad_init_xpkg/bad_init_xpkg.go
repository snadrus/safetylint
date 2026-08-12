package bad_init_xpkg

import "plainlib"

func init() {
	go func() {
		plainlib.X++ // want `init goroutine writes foreign global`
	}()
}
