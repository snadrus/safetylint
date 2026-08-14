package good_execwait

import (
	"os/exec"
	"sync"
)

// pdp_guide tunnel shape: one goroutine Waits on the command while Stop
// signals via cmd.Process under the mutex that also guards the global.
var (
	tunnelMu  sync.Mutex
	tunnelCmd *exec.Cmd
)

func Start() error {
	tunnelMu.Lock()
	defer tunnelMu.Unlock()

	cmd := exec.Command("sleep", "1")
	if err := cmd.Start(); err != nil {
		return err
	}
	tunnelCmd = cmd

	go func() {
		_ = cmd.Wait()
		tunnelMu.Lock()
		if tunnelCmd == cmd {
			tunnelCmd = nil
		}
		tunnelMu.Unlock()
	}()
	return nil
}

func Stop() {
	tunnelMu.Lock()
	defer tunnelMu.Unlock()
	if tunnelCmd != nil && tunnelCmd.Process != nil {
		_ = tunnelCmd.Process.Kill()
	}
	tunnelCmd = nil
}
