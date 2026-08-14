package good_broadcast

import "sync"

type Conn interface {
	SendMessage([]byte) error
}

type peer struct {
	id    int64
	gen   uint64
	addr  string
	conn  Conn
	tasks map[string]struct{}
}

type Registry struct { // want Registry:"concurrentSafe"
	mu     sync.RWMutex
	peers  map[int64]peer
	byTask map[string]map[int64]struct{}
}

type ErrorHook func(addr string, err error)

// Curio shape: map lookup + value capture under RLock.
func (r *Registry) Broadcast(taskType string, msg []byte, errFn ErrorHook) { // want Broadcast:"mayShareParams param1:read"
	r.mu.RLock()
	defer r.mu.RUnlock()
	for id := range r.byTask[taskType] {
		p, ok := r.peers[id]
		if !ok {
			continue
		}
		go func() {
			if err := p.conn.SendMessage(msg); err != nil && errFn != nil {
				errFn(p.addr, err)
			}
		}()
	}
}

type memConn struct{}

func (memConn) SendMessage(b []byte) error { return nil }

func Run() {
	r := &Registry{
		peers:  map[int64]peer{1: {id: 1, addr: "a", conn: memConn{}, tasks: map[string]struct{}{"X": {}}}},
		byTask: map[string]map[int64]struct{}{"X": {1: {}}},
	}
	r.Broadcast("X", []byte("hi"), func(string, error) {})
}
