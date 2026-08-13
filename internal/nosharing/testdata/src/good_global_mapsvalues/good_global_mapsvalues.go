package good_global_mapsvalues

import "maps"

var m = map[string]int{"a": 1}

func Run() int {
	go func() {}()
	n := 0
	for range maps.Values(m) {
		n++
	}
	return n
}
