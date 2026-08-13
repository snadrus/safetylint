package bad_dyngo

type node struct {
	onConnect func(string)
}

func (n *node) SetOnConnect(fn func(string)) {
	n.onConnect = fn
}

// No same-package assignment of a concrete body — dyn go must fail.
func Kick(n *node) {
	if n.onConnect != nil {
		go n.onConnect("x") // want `goroutine with non-static callee`
	}
}
