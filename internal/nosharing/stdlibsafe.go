package nosharing

import (
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// operandEffect is how a curated stdlib/builtin call treats one operand.
type operandEffect uint8

const (
	effectUnknown operandEffect = iota // not in the table
	effectNone                         // ignored (offsets, counts)
	effectRead                         // read-only use of caller memory
	effectWrite                        // may write through the operand
	effectSafe                         // ConcurrentSafe sink (e.g. *os.File)
)

// stdlibShape is the per-operand effect of a stdlib function or method.
// For methods, Recv is the receiver and Args are the remaining parameters.
type stdlibShape struct {
	recv operandEffect
	args []operandEffect
}

// stdlibEffects classifies bodyless GOROOT calls that would otherwise be
// treated as pessimistic writes of every pointer argument.
//
// Key is "pkg.Func" or "(*pkg.Type).Method".
//
//	Call                         recv   args
//	io.ReadFull(r, buf)          —      read, write     // dest []byte
//	io.ReadAtLeast(r, buf, n)    —      read, write, —
//	io.Copy(dst, src)            —      write, read     // dst may be File (Safe)
//	io.CopyBuffer(d, s, buf)     —      write, read, write
//	io.CopyN(dst, src, n)        —      write, read, —
//	io.ReadAll(r)                —      read
//	io.WriteString(w, s)         —      write, read
//	(*os.File).Read(p)           Safe   write
//	(*os.File).ReadAt(p, off)    Safe   write, —
//	(*os.File).Write(p)          Safe   read
//	(*os.File).WriteAt(p, off)   Safe   read, —         // treed_build persist
//	(*os.File).ReadFrom(r)       Safe   read
//	(*os.File).WriteTo(w)        Safe   write
//	(*os.File).Close/Sync/…      Safe   —
//	copy(dst, src)               —      write, read     // builtin
//	append(dst, …)               —      write, read…
//	len/cap                      —      read
//	hash.Hash.Write (invoke)     —      read payload
//	io.Reader.Read (invoke)      —      write dest
//
// Facts produced elsewhere: WritesParams (recv/paramN written) on
// module-cache callees; ConcurrentSafe on File/Client/Server/DB/Context.
var stdlibEffects = map[string]stdlibShape{
	"io.ReadFull":    {args: []operandEffect{effectRead, effectWrite}},
	"io.ReadAtLeast": {args: []operandEffect{effectRead, effectWrite, effectNone}},
	"io.Copy":        {args: []operandEffect{effectWrite, effectRead}},
	"io.CopyBuffer":  {args: []operandEffect{effectWrite, effectRead, effectWrite}},
	"io.CopyN":       {args: []operandEffect{effectWrite, effectRead, effectNone}},
	"io.ReadAll":     {args: []operandEffect{effectRead}},
	"io.WriteString": {args: []operandEffect{effectWrite, effectRead}},
	"io.NopCloser":   {args: []operandEffect{effectRead}},
	"io.LimitReader": {args: []operandEffect{effectRead, effectNone}},
	"io.MultiReader": {args: []operandEffect{effectRead}},
	"io.MultiWriter": {args: []operandEffect{effectWrite}},
	"io.TeeReader":   {args: []operandEffect{effectRead, effectWrite}},

	"(*os.File).Read":     {recv: effectSafe, args: []operandEffect{effectWrite}},
	"(*os.File).ReadAt":   {recv: effectSafe, args: []operandEffect{effectWrite, effectNone}},
	"(*os.File).Write":    {recv: effectSafe, args: []operandEffect{effectRead}},
	"(*os.File).WriteAt":  {recv: effectSafe, args: []operandEffect{effectRead, effectNone}},
	"(*os.File).ReadFrom": {recv: effectSafe, args: []operandEffect{effectRead}},
	"(*os.File).WriteTo":  {recv: effectSafe, args: []operandEffect{effectWrite}},
	"(*os.File).Close":    {recv: effectSafe},
	"(*os.File).Sync":     {recv: effectSafe},
	"(*os.File).Truncate": {recv: effectSafe, args: []operandEffect{effectNone}},
	"(*os.File).Seek":     {recv: effectSafe, args: []operandEffect{effectNone, effectNone}},
	"(*os.File).Stat":     {recv: effectSafe},
	"(*os.File).Name":     {recv: effectSafe},
	"(*os.File).Fd":       {recv: effectSafe},

	"(*bytes.Buffer).Write":       {recv: effectWrite, args: []operandEffect{effectRead}},
	"(*bytes.Buffer).WriteString": {recv: effectWrite, args: []operandEffect{effectRead}},
	"(*bytes.Buffer).WriteByte":   {recv: effectWrite, args: []operandEffect{effectNone}},
	"(*bytes.Buffer).Read":        {recv: effectWrite, args: []operandEffect{effectWrite}},
	"(*bytes.Buffer).Next":        {recv: effectWrite, args: []operandEffect{effectNone}},
	"(*bytes.Buffer).Bytes":       {recv: effectRead},
	"(*bytes.Buffer).String":      {recv: effectRead},
	"(*bytes.Buffer).Len":         {recv: effectRead},
	"(*bytes.Reader).Read":        {recv: effectWrite, args: []operandEffect{effectWrite}},
	"(*bytes.Reader).ReadAt":      {recv: effectRead, args: []operandEffect{effectWrite, effectNone}},
	"(*bytes.Reader).WriteTo":     {recv: effectRead, args: []operandEffect{effectWrite}},

	"(*strings.Reader).Read":    {recv: effectWrite, args: []operandEffect{effectWrite}},
	"(*strings.Reader).ReadAt":  {recv: effectRead, args: []operandEffect{effectWrite, effectNone}},
	"(*strings.Reader).WriteTo": {recv: effectRead, args: []operandEffect{effectWrite}},

	"(*bufio.Reader).Read":  {recv: effectWrite, args: []operandEffect{effectWrite}},
	"(*bufio.Writer).Write": {recv: effectWrite, args: []operandEffect{effectRead}},
	"(*bufio.Writer).Flush": {recv: effectWrite},
}

// stdlibReadOnly lists bodyless GOROOT helpers that only read map/slice
// arguments (passing a loaded global header is not a freeze write).
var stdlibReadOnly = map[string]map[string]bool{
	"time": {
		"Since": true, "Until": true,
	},
	"maps": {
		"All": true, "Keys": true, "Values": true,
		"equal": true, "equalFunc": true,
	},
	"slices": {
		"All": true, "Values": true,
		"Contains": true, "ContainsFunc": true,
		"Index": true, "IndexFunc": true,
		"BinarySearch": true, "BinarySearchFunc": true,
		"Clone": true, "Concat": true,
		"equal": true, "equalFunc": true,
		"Compare": true, "CompareFunc": true,
		"IsSorted": true, "IsSortedFunc": true,
		"Max": true, "MaxFunc": true, "Min": true, "MinFunc": true,
	},
}

func init() {
	// Go's exported names are capitalized; accept both for robustness.
	for _, byName := range stdlibReadOnly {
		for name := range byName {
			if name != "" && name[0] >= 'a' && name[0] <= 'z' {
				cap := strings.ToUpper(name[:1]) + name[1:]
				byName[cap] = true
			}
		}
	}
}

func isAtomicCallee(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	return calleePkgPath(fn) == "sync/atomic"
}

func isStdlibReadOnlyCall(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	// Prefer the generic origin so instantiated maps.Values[...] matches.
	if orig := fn.Origin(); orig != nil {
		fn = orig
	}
	byName := stdlibReadOnly[calleePkgPath(fn)]
	if byName == nil {
		return false
	}
	name := fn.Name()
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.IndexByte(name, '['); i >= 0 {
		name = name[:i]
	}
	return byName[name]
}

func stdlibShapeKey(fn *ssa.Function) string {
	if fn == nil {
		return ""
	}
	if orig := fn.Origin(); orig != nil {
		fn = orig
	}
	pkg := calleePkgPath(fn)
	name := fn.Name()
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.IndexByte(name, '['); i >= 0 {
		name = name[:i]
	}
	if recv := fn.Signature.Recv(); recv != nil {
		rt := types.Unalias(recv.Type())
		star := ""
		if p, ok := rt.(*types.Pointer); ok {
			star = "*"
			rt = types.Unalias(p.Elem())
		}
		tname := ""
		if n, ok := rt.(*types.Named); ok && n.Obj() != nil {
			tname = n.Obj().Name()
		}
		return "(" + star + pkg + "." + tname + ")." + name
	}
	if pkg == "" {
		return name
	}
	return pkg + "." + name
}

func lookupStdlibShape(fn *ssa.Function) (stdlibShape, bool) {
	if fn == nil {
		return stdlibShape{}, false
	}
	sh, ok := stdlibEffects[stdlibShapeKey(fn)]
	return sh, ok
}

func shapeEffectForArg(sh stdlibShape, argIndex int, hasRecv bool) operandEffect {
	if hasRecv {
		if argIndex == 0 {
			if sh.recv == 0 {
				return effectNone
			}
			return sh.recv
		}
		argIndex--
	}
	if argIndex < 0 {
		return effectNone
	}
	if argIndex < len(sh.args) {
		return sh.args[argIndex]
	}
	if len(sh.args) > 0 {
		return sh.args[len(sh.args)-1]
	}
	return effectNone
}

// stdlibOperandEffect reports a table hit for how c treats v.
func stdlibOperandEffect(c *ssa.CallCommon, v ssa.Value) (operandEffect, bool) {
	if c == nil || v == nil {
		return effectUnknown, false
	}
	if !c.IsInvoke() {
		if b, ok := c.Value.(*ssa.Builtin); ok {
			switch b.Name() {
			case "copy":
				if len(c.Args) > 0 && c.Args[0] == v {
					return effectWrite, true
				}
				if len(c.Args) > 1 && c.Args[1] == v {
					return effectRead, true
				}
				return effectNone, true
			case "append", "clear", "delete":
				if len(c.Args) > 0 && c.Args[0] == v {
					return effectWrite, true
				}
				if argOrRecvIs(c, v) {
					return effectRead, true
				}
				return effectNone, true
			case "len", "cap":
				if argOrRecvIs(c, v) {
					return effectRead, true
				}
				return effectNone, true
			}
		}
	}
	cal := c.StaticCallee()
	if cal == nil {
		return invokeOperandEffect(c, v)
	}
	sh, ok := lookupStdlibShape(cal)
	if !ok {
		return effectUnknown, false
	}
	hasRecv := cal.Signature.Recv() != nil
	for i, arg := range c.Args {
		if arg != v {
			continue
		}
		return shapeEffectForArg(sh, i, hasRecv), true
	}
	return effectNone, true
}

func invokeOperandEffect(c *ssa.CallCommon, v ssa.Value) (operandEffect, bool) {
	if c == nil || !c.IsInvoke() || c.Method == nil {
		return effectUnknown, false
	}
	name := c.Method.Name()
	if c.Value == v {
		return effectNone, true
	}
	switch name {
	case "Read", "ReadAt":
		if argOrRecvIs(c, v) && isReadOnlyPayloadType(v.Type()) {
			return effectWrite, true
		}
	case "Write", "WriteAt", "WriteTo", "WriteString":
		if argOrRecvIs(c, v) && isReadOnlyPayloadType(v.Type()) {
			return effectRead, true
		}
	}
	return effectUnknown, false
}

func addStdlibCallAccesses(instr ssa.Instruction, c *ssa.CallCommon, cal *ssa.Function, derived map[ssa.Value]bool, add func(ssa.Instruction, ssa.Value, bool)) bool {
	sh, ok := lookupStdlibShape(cal)
	if !ok {
		return false
	}
	hasRecv := cal.Signature.Recv() != nil
	for i, arg := range c.Args {
		if !derived[arg] || isMutexFieldAddr(arg) {
			continue
		}
		switch shapeEffectForArg(sh, i, hasRecv) {
		case effectWrite:
			add(instr, arg, true)
		case effectRead:
			add(instr, arg, false)
		}
	}
	return true
}
