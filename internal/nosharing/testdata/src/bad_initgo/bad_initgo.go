// want package:"hotGlobals g:read"
package bad_initgo

var g int

func init() {
	go func() {
		_ = g
	}()
}

func Run() {
	g = 1 // want `write to package global g`
}
