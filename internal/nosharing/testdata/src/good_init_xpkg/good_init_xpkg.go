package good_init_xpkg

import "hotlib"

func init() {
	go func() {
		hotlib.St.Mu.Lock()
		hotlib.St.N++
		hotlib.St.Mu.Unlock()
	}()
}
