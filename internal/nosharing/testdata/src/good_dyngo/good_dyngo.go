package good_dyngo

type node struct {
	onConnect func(string)
}

func (n *node) SetOnConnect(fn func(string)) {
	n.onConnect = fn
}

func greet(addr string) {}

func Run() {
	n := &node{}
	n.SetOnConnect(greet)
	remote := n
	if remote.onConnect != nil {
		go remote.onConnect("peer")
	}
}
