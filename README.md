# safetylint

What if Golang were magically Rust-like memory-safe with no code changes?
You're welcome. 

A Go linter that **proves a program is memory-safe** under a
strict sharing discipline. Add it to your CI. 

Go without `unsafe` / cgo is memory-safe when single-threaded, but not when
goroutines share mutable memory. safetylint refuses the escape hatches and
then refuses cross-goroutine sharing except via channels with
**freeze-after-send** for pointer-carrying values, or via a **proven
always-held `sync.Mutex`** embedded in the same (or parent) struct as the data.

Note: *Exotic* mutex usage simply fails. Just like Escape analysis, 
if you feel like getting complicated, I just haven't implemented your case yet. PRs are welcomed. Keep mutexes near their data. Keep things in-package. 

## Install / run

```bash
go install safetylint/cmd/safetylint@latest   # once published
# or from this repo:
go build -o safetylint ./cmd/safetylint
./safetylint ./...
```

Exit status is non-zero if any diagnostic is reported (via `multichecker`).

## What it proves

When safetylint accepts a package, the following hold (soundness: when in
doubt, it **rejects**):

1. **No language escape hatches** (`nounsafe`)
   - no `import "unsafe"`
   - no `import "C"` / cgo
   - no `.s` assembly in the package
   - no `//go:linkname` or `//go:cgo_*` directives
   - no `reflect` pointer laundering (`UnsafePointer`, `SliceHeader`, …)
   - Escape-hatch hits report that the code is **not verified**: check its
     safety and the adapter's safety yourself.

2. **No racy shared memory** (`nosharing`)
   - Memory shared with a goroutine via capture, argument, or global must be
     **provably read-only**, be `*sync.WaitGroup` / `*sync.Once` / `*sync.Mutex`,
     or be guarded by **one tied** `sync.Mutex` field in the **same or parent
     struct** that is **always locked** at every access (reads and writes).
     Different mutex fields of the same struct are not interchangeable.
     Writes through separately heap-allocated objects reached only via
     pointer fields of the shared value do not count as writing that value
     (e.g. `cfg.DB.Query` does not write `*Cfg`); module-cache callees export
     `WritesParams` Facts so third-party pointer calls can be evaluated.
   - `mu.TryLock()` only counts as an acquire on CFG paths where its boolean
     result is proven true (e.g. `if mu.TryLock() { ... }` or
     `ok := mu.TryLock(); if !ok { return }; ...`).
   - Values may move between goroutines through **channels**.
   - If a sent value contains pointers, those pointees are **frozen after
     send**: neither sender nor receiver may write through them afterward.
   - Pointer-free values copied by a channel may be mutated freely after send.
   - `sync.RWMutex`-guarded sharing is **refused** (`sync.Mutex` is just as
     fast; or use channels).

3. **Globals are init-then-freeze** (`nosharing`)

   In any package that spawns goroutines, package globals may be written only
   where the write **provably happens before any goroutine could be running**:
   - in `init` functions and package `var` initializers,
   - in `main` (for `package main`) before the first *spawn point*,
   - in helpers provably called **only** from such pre-spawn points
     (unexported or in `package main`, address never taken, never a
     goroutine body).

   A *spawn point* is any `go` statement, any dynamic/interface call, any
   Fact-bearing spawner (`MaySpawn` / `MayShareParams`), or a curated stdlib
   server API (`http.ListenAndServe`, `Serve`, …) — not every Fact-less
   cross-package call. After the first spawn point, globals are frozen:
   **reads stay legal**, and writes are refused unless the global is a struct
   with an embedded `sync.Mutex` whose accesses are all proven locked.

4. **Cross-package share Facts** (`nosharing`)

   Exported functions that spawn and retain parameters publish `MayShareParams`
   Facts (mode `read`/`write`, optional tied mutex). Call sites are synthetic
   share events: post-call writes are refused unless under the Fact's tied
   `sync.Mutex`. Wrappers re-export Facts from callees. Stdlib / GOROOT
   packages are not Fact-analyzed (treated as unknown: no assumed retention;
   curated Serve/Listen APIs remain freeze spawn points).

   Curated async stdlib APIs (`time.AfterFunc`, `http.HandleFunc` / `Handle`,
   `os/signal.NotifyFunc`) treat callback closures as share events on their
   captures.

   If **init** starts concurrency, `main` and other non-init code are already
   post-spawn for global freeze. Globals touched by init-time goroutines are
   published as package `HotGlobals` Facts (mode + optional tied mutex).
   Init goroutines may **read** another package's frozen globals freely (not
   listed as `HotGlobals`, or hot read-only). Writes, and reads of write-hot
   globals, require that package's `HotGlobals` tied `sync.Mutex` held at the
   access (untraced cross-package init sharing otherwise).

### Proven `sync.Mutex` example

```go
type Counter struct {
    mu sync.Mutex
    n  int
}

func (c *Counter) Inc() {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.n++
}
```

A free-standing `sync.Mutex` beside unrelated data (not a field of the same
struct) does **not** count as a guard. If a struct has multiple mutex fields,
every access of a shared field must use the **same** tied mutex.

## Examples

```bash
./safetylint ./examples/safe/...          # clean
./safetylint ./examples/unsafe_race/...   # reports shared-memory write
```

## Tests

```bash
go test ./...
```

Corpora live under `internal/*/testdata/src/...` and use `analysistest`
`// want` annotations.

## Limitations (over-approximation)

safetylint prefers false rejections over missed races:

- Mutex proofs are **intra-procedural**: the lock and the access must be in
  the same function (caller-held locks are not inferred). Across functions,
  one **tied** mutex field (same struct type + field) must protect every
  touchpoint of the shared memory.
- `TryLock` is not treated as an unconditional acquire; only paths where the
  result is proven true acquire the guard.
- Only **struct-embedded** `sync.Mutex` fields guard data; free-standing
  mutexes do not.
- `sync.RWMutex` is never accepted as a guard (`sync.Mutex` is just as fast).
- Ownership hand-off where the **receiver** mutates channel-sent pointers is
  rejected (freeze-after-send, not transfer-of-mutability).
- Dynamic `go` callees (interfaces / function values) are rejected.
- Cross-package callees without a `MayShareParams` Fact are treated
  pessimistically for argument writes. Fact-less cross-package calls do
  **not** end the global-write phase; `go`, dynamic/interface calls,
  Fact-bearing spawners, and curated Serve/Listen/async APIs do.
- Stdlib / GOROOT packages are skipped for Fact export; curated async APIs
  (`time.AfterFunc`, `http.HandleFunc`, …) still share callback captures.
  Other hidden stdlib spawn+retain APIs remain a limitation until listed.
  Running on a Go toolchain newer than this tool's verified version warns
  that faults via new standard funcs may be possible.
- Exported functions in library packages may never write plain globals once
  the package spawns goroutines anywhere: other packages could call them
  concurrently.
- Alias analysis is conservative SSA def-use, not a full points-to analysis.

## Architecture

| Piece | Role |
|-------|------|
| `cmd/safetylint` | `multichecker` driver |
| `internal/nounsafe` | AST / package checks for escape hatches |
| `internal/nosharing` | SSA analysis of goroutine sharing, channel freeze, mutex guards, cross-package share Facts |

```
safetylint ./...
    ├─ nounsafe   (inspect AST)
    └─ nosharing  (buildssa → spawn roots → writes / freeze / mutex / Facts)
```
