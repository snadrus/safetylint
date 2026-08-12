// Stub for analysistest GOPATH mode (real module used outside testdata).
package singleflight

type Group struct{}

func (g *Group) Do(key string, fn func() (any, error)) (any, error, bool) {
	v, err := fn()
	return v, err, false
}

func (g *Group) DoChan(key string, fn func() (any, error)) <-chan struct {
	Val    any
	Err    error
	Shared bool
} {
	ch := make(chan struct {
		Val    any
		Err    error
		Shared bool
	}, 1)
	v, err, shared := g.Do(key, fn)
	ch <- struct {
		Val    any
		Err    error
		Shared bool
	}{v, err, shared}
	return ch
}

func (g *Group) Forget(key string) {}
