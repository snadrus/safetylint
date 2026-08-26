package good_afterfunc_mutex

import (
	"sync"
	"time"
)

// Tied mutex: caller keeps the hold across AfterFunc and writes afterward;
// the callback re-locks the same field at every access.
type engine struct {
	mu     sync.Mutex
	timers map[string]*time.Timer
	n      int
}

func (e *engine) arm(key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var t *time.Timer
	t = time.AfterFunc(time.Millisecond, func() {
		e.mu.Lock()
		if e.timers[key] == t {
			delete(e.timers, key)
		}
		e.n++
		e.mu.Unlock()
	})
	e.timers[key] = t
	e.n++
}

// Free-standing local mutex matching the scheduler bundler pattern:
// hold across AfterFunc, more map writes under the same lock; callback
// re-locks at every access.
func bundler() (func(string), <-chan string) {
	timers := make(map[string]*time.Timer)
	timerMx := sync.Mutex{}
	output := make(chan string)
	return func(taskType string) {
		timerMx.Lock()
		defer timerMx.Unlock()
		if t, ok := timers[taskType]; ok {
			t.Reset(time.Millisecond)
			return
		}
		var t *time.Timer
		t = time.AfterFunc(time.Millisecond, func() {
			timerMx.Lock()
			if timers[taskType] == t {
				delete(timers, taskType)
			}
			timerMx.Unlock()
			output <- taskType
		})
		timers[taskType] = t
	}, output
}
