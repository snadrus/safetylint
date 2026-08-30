package good_escape_reg

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
	r.Cpu = 2
	r.MachineID = 1
	go func() {
		for {
			if r.shutdown.Load() {
				return
			}
			_ = r.MachineID
			_ = r.Cpu
			return
		}
	}()
	return &r
}

func (r *Reg) Shutdown() {
	r.shutdown.Store(true)
}

func Run() {
	r := Register()
	r.Shutdown()
}
