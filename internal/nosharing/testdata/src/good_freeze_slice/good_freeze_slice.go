package good_freeze_slice

var table = []string{"a", "b"}

func contains(msg string, parts []string) bool {
	for _, p := range parts {
		if msg == p {
			return true
		}
	}
	return false
}

func check(msg string) bool {
	return contains(msg, table)
}

func Run() {
	go func() { _ = check("a") }()
	_ = check("b")
}
