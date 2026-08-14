package good_concurrentsafe

import "sync"

type SafeBox struct { // want SafeBox:"concurrentSafe"
	mu sync.Mutex
	n  int
}

func (s *SafeBox) Inc() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
}

func Run(s *SafeBox) { // want Run:"mayShareParams param0:write.*"
	go s.Inc()
	s.Inc()
}
