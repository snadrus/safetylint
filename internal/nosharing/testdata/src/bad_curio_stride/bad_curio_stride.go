package bad_curio_stride

func pad(in, out []byte) {
	copy(out, in)
}

func Run(n int) {
	unpadded := make([]byte, n*127)
	padded := make([]byte, n*128)
	k := (n + 3) / 4
	for w := range 4 {
		start := w * k
		end := min((w+1)*k, n)
		_ = start
		_ = end
		go func() { // want `shared memory` `shared memory`
			// Whole-buffer helper args are not a stride tile.
			pad(unpadded, padded)
		}()
	}
}
