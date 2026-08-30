package bad_once_goro_handler

import (
	"context"
	"sync"
	"time"
)

type Provider struct {
	startLock    sync.Once
	announceURLs []string
	latest       map[string]int
	mu           sync.Mutex
}

func (p *Provider) Start(ctx context.Context) {
	p.startLock.Do(func() {
		go p.publish(ctx) // want `shared memory .* written without channel transfer`
	})
}

func (p *Provider) publish(ctx context.Context) {
	p.latest["a"] = 1
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			p.latest["a"] = 2
		case <-ctx.Done():
			return
		}
	}
}

func (p *Provider) handleGet() {
	_ = p.latest["a"]
}

func Run() {
	p := &Provider{latest: map[string]int{}}
	p.Start(context.Background())
	go p.handleGet()
}
