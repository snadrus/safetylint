package nosharing

import (
	"fmt"
	"strings"
)

// ShareMode describes how a callee may concurrently access a retained param
// after returning to the caller.
type ShareMode string

const (
	ShareRead  ShareMode = "read"
	ShareWrite ShareMode = "write"
)

// MayShareParams is an object Fact on *types.Func: the function may return
// while a goroutine still shares the listed parameters/receiver.
type MayShareParams struct {
	Params []SharedParam
}

func (*MayShareParams) AFact() {}

func (f *MayShareParams) String() string {
	if f == nil || len(f.Params) == 0 {
		return "mayShareParams"
	}
	var b strings.Builder
	b.WriteString("mayShareParams")
	for _, p := range f.Params {
		b.WriteByte(' ')
		if p.Recv {
			b.WriteString("recv")
		} else {
			fmt.Fprintf(&b, "param%d", p.Index)
		}
		b.WriteByte(':')
		b.WriteString(string(p.Mode))
		if p.Mutex.StructPath != "" {
			fmt.Fprintf(&b, "+mu[%s#%d]", p.Mutex.StructPath, p.Mutex.Field)
		}
	}
	return b.String()
}

// SharedParam identifies a parameter (or receiver) retained by a spawned
// goroutine after the function returns.
type SharedParam struct {
	Index int  // Signature.Params index; ignored when Recv is true
	Recv  bool // shared object is the method receiver
	Mode  ShareMode
	// Mutex is the single tied sync.Mutex field proven to guard this param
	// inside the defining package. Zero ⇒ no proven guard.
	Mutex MutexField
}

// MutexField is a gob-safe identity for a struct-embedded sync.Mutex.
type MutexField struct {
	StructPath string // types.Type.String() of the struct (usually underlying)
	Field      int
}

func (m MutexField) set() bool {
	return m.StructPath != ""
}

// MaySpawn is an object Fact on *types.Func: the function may start a
// goroutine (even if it retains no parameters). Used for global-freeze
// spawn-point precision alongside MayShareParams.
type MaySpawn struct{}

func (*MaySpawn) AFact() {}

func (*MaySpawn) String() string { return "maySpawn" }

// HotGlobals is a package Fact: globals that init-time goroutines may
// concurrently access after init returns (and thus after main starts).
// Writers must use the tied mutex when set, or avoid post-concurrency writes.
type HotGlobals struct {
	Globals []HotGlobal
}

func (*HotGlobals) AFact() {}

func (f *HotGlobals) String() string {
	if f == nil || len(f.Globals) == 0 {
		return "hotGlobals"
	}
	var b strings.Builder
	b.WriteString("hotGlobals")
	for _, g := range f.Globals {
		fmt.Fprintf(&b, " %s:%s", g.Name, g.Mode)
		if g.Mutex.set() {
			fmt.Fprintf(&b, "+mu[%s#%d]", g.Mutex.StructPath, g.Mutex.Field)
		}
	}
	return b.String()
}

// HotGlobal describes one package global touched by init-time concurrency.
type HotGlobal struct {
	Name  string // Global.Name() within the defining package
	Mode  ShareMode
	Mutex MutexField
}

// WritesParams is an object Fact on *types.Func: which parameters (and
// receiver) the function may write through. Exported for module-cache
// packages so callers can evaluate third-party pointer calls precisely
// instead of assuming every cross-package call writes.
type WritesParams struct {
	Recv   bool  // method receiver may be written
	Params []int // signature parameter indices that may be written
}

func (*WritesParams) AFact() {}

func (f *WritesParams) String() string {
	if f == nil {
		return "writesParams"
	}
	var b strings.Builder
	b.WriteString("writesParams")
	if f.Recv {
		b.WriteString(" recv")
	}
	for _, i := range f.Params {
		fmt.Fprintf(&b, " param%d", i)
	}
	if !f.Recv && len(f.Params) == 0 {
		b.WriteString(" none")
	}
	return b.String()
}

func (f *WritesParams) writesArg(recv bool, index int) bool {
	if f == nil {
		return false
	}
	if recv {
		return f.Recv
	}
	for _, i := range f.Params {
		if i == index {
			return true
		}
	}
	return false
}
