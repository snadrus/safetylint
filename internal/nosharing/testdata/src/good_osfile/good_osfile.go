package good_osfile

import "os"

func Run(f *os.File) { // want Run:"mayShareParams param0:read"
	go func() {
		_, _ = f.Write([]byte("x"))
	}()
	_, _ = f.Write([]byte("y"))
}

// WriteAt reads the payload; concurrent WriteAt of the same []byte is sharing a read.
func WriteAtShare(f *os.File, buf []byte) { // want WriteAtShare:"mayShareParams param0:read param1:read"
	go func() {
		_, _ = f.WriteAt(buf, 0)
	}()
	_, _ = f.WriteAt(buf, 8)
}

// copy's source is a read; concurrent copies from one buffer are read-only sharing.
func CopySrcShare(buf []byte) { // want CopySrcShare:"mayShareParams param0:read"
	go func() {
		dst := make([]byte, len(buf))
		copy(dst, buf)
	}()
	dst := make([]byte, len(buf))
	copy(dst, buf)
}
