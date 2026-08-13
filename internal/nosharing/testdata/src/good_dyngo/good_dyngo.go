package good_dyngo

type node struct {
	onConnect func(string)
}

// Unexported setter: same-package assignments complete the dynamic-go set.
// An exported setter would accept foreign funcs and must stay unresolved.
func (n *node) setOnConnect(fn func(string)) {
	n.onConnect = fn
}

func greet(addr string) {}

func Run() {
	n := &node{}
	n.setOnConnect(greet)
	remote := n
	if remote.onConnect != nil {
		go remote.onConnect("peer")
	}
}
