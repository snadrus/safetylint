package bad_escape_reg

import "sync/atomic"

type Resources struct {
	Cpu       int
	MachineID int
}

type Reg struct {
	Resources
	shutdown atomic.Bool
}

func Register() *Reg {
	var r Reg
	r.MachineID = 1
	go func() { // want `shared memory .* written without channel transfer`
		_ = r.shutdown.Load()
		r.MachineID++
	}()
	return &r
}

func Run() {
	_ = Register()
}
