package bad_afterfunc

import "time"

func Run() {
	n := 0
	time.AfterFunc(time.Millisecond, func() { // want `shared memory .* written without channel transfer`
		n++
	})
	n++
}
