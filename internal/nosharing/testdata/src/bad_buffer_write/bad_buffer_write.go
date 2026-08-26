package bad_buffer_write

import "bytes"

func Run() {
	var buf bytes.Buffer
	go func() { // want `shared memory .* written without channel transfer and no proven lock/atomic/partition guard`
		buf.Write([]byte("x"))
	}()
	buf.Write([]byte("y"))
}
