// want package:"hotGlobals St:write"
package hotlib

import "sync"

type S struct {
	Mu sync.Mutex
	N  int
}

var St S

func init() {
	go func() {
		St.Mu.Lock()
		St.N++
		St.Mu.Unlock()
	}()
}
