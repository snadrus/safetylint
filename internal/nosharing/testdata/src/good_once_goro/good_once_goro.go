package good_once_goro

import "sync"

type Provider struct {
	startLock    sync.Once
	announceURLs []string
	latest       map[string]int
	mu           sync.Mutex
}

func (p *Provider) Start() {
	p.startLock.Do(func() {
		go p.publish()
	})
}

func (p *Provider) publish() {
	p.announceURLs = p.announceURLs[:len(p.announceURLs):len(p.announceURLs)]
	p.latest["a"] = 1
	for {
		_ = p.announceURLs
		p.mu.Lock()
		_ = p.latest["a"]
		p.mu.Unlock()
		return
	}
}

func Run() {
	p := &Provider{announceURLs: []string{"x"}, latest: map[string]int{}}
	p.Start()
}
