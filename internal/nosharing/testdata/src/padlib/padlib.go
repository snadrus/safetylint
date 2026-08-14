package padlib

// Pad expands in into out (fr32.Pad shape): writes out, reads in.
func Pad(in, out []byte) { // want Pad:"writesParams param1"
	for i := range out {
		out[i] = 0
	}
	copy(out, in)
}
