package good_filprice

import (
	"io"
	"net/http"
	"sync"
	"time"
)

var (
	priceMu     sync.Mutex
	priceCached float64
	priceAt     time.Time
)

func Price() float64 {
	priceMu.Lock()
	defer priceMu.Unlock()
	if priceCached > 0 && time.Since(priceAt) < time.Minute {
		return priceCached
	}
	resp, err := http.Get("http://example.com")
	if err != nil {
		return priceCached
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	priceCached = 1.0
	priceAt = time.Now()
	return priceCached
}

func Run() {
	go func() { _ = Price() }()
	_ = Price()
}
