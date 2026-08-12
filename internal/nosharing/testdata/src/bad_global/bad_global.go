package bad_global

var shared int

func Start() {
	go func() {
		shared = 1 // want `write to package global shared`
	}()
	shared = 2 // want `write to package global shared`
}
