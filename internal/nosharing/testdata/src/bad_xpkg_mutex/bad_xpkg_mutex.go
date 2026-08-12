package bad_xpkg_mutex

import "sharemu"

func Run() {
	s := &sharemu.S{}
	sharemu.Start(s) // want `shared memory from Fact-bearing call written/accessed without its tied sync.Mutex guard`
	s.N++            // unlocked
}
