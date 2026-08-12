package good_xpkg_mutex

import "sharemu"

func Run() {
	s := &sharemu.S{}
	sharemu.Start(s)
	s.Mu.Lock()
	s.N++
	s.Mu.Unlock()
}
