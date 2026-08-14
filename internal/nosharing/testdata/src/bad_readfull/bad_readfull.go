package bad_readfull

import "io"

// io.ReadFull and io.Reader.Read write the destination []byte.
func RaceFull(r io.Reader, buf []byte) { // want RaceFull:"mayShareParams param0:read param1:write"
	go func() { // want `shared memory`
		_, _ = io.ReadFull(r, buf)
	}()
	_, _ = io.ReadFull(r, buf)
}

func RaceRead(r io.Reader, buf []byte) { // want RaceRead:"mayShareParams param0:read param1:write"
	go func() { // want `shared memory`
		_, _ = r.Read(buf)
	}()
	_, _ = r.Read(buf)
}
