package good_global_read

import (
	"io/fs"
	"strings"
	"testing/fstest"
)

var label string
var files fs.FS

func init() {
	files = fstest.MapFS{"a.txt": &fstest.MapFile{Data: []byte("x")}}
}

func main() {
	// Fact-less cross-package calls are not freeze spawn points.
	label = strings.ToUpper("ok")
	go func() {
		_ = label
	}()
	// Reads (including passing a loaded FS to Sub) stay legal after spawn.
	sub, err := fs.Sub(files, ".")
	if err != nil {
		return
	}
	_ = sub
	_ = label
}
