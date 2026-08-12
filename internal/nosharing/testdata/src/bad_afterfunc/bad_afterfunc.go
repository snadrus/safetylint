package bad_afterfunc

import "time"

func Run() {
	n := 0
	time.AfterFunc(time.Millisecond, func() { // want `shared memory from Fact-bearing call written`
		n++
	})
	n++
}
