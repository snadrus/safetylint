package good_rwcache

import (
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

var (
	cacheMu sync.RWMutex
	cache   struct {
		at time.Time
		v  int
		ok bool
	}
	sf singleflight.Group
)

func Get() int {
	cacheMu.RLock()
	if cache.ok && time.Since(cache.at) < time.Minute {
		v := cache.v
		cacheMu.RUnlock()
		return v
	}
	cacheMu.RUnlock()

	v, _, _ := sf.Do("k", func() (any, error) {
		cacheMu.Lock()
		cache.v = 1
		cache.at = time.Now()
		cache.ok = true
		cacheMu.Unlock()
		return 1, nil
	})
	return v.(int)
}

func Run() {
	go func() { _ = Get() }()
	_ = Get()
}
