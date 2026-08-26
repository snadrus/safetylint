package good_afterfunc

import "time"

func Run() {
	n := 42
	time.AfterFunc(time.Millisecond, func() {
		_ = n
	})
}
