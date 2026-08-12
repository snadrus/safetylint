package bad_byte_mutate

// Bodyless mutate must count as a write to []byte (not read-only payload).
func mutate(b []byte)

func Race(b []byte) { // want Race:"mayShareParams param0:write"
	go func() { // want `shared memory` `shared memory`
		mutate(b)
	}()
	mutate(b)
}
