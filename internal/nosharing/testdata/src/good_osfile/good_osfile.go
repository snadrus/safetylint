package good_osfile

import "os"

func Run(f *os.File) { // want Run:"mayShareParams param0:read"
	go func() {
		_, _ = f.Write([]byte("x"))
	}()
	_, _ = f.Write([]byte("y"))
}
