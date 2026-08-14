package bad_frozen_store

type db struct{ id int }

type Svc struct {
	db *db
	n  int
}

func New() *Svc {
	s := &Svc{db: &db{id: 1}}
	go s.loop()       // want `shared memory`
	s.db = &db{id: 2} // store after publish
	return s
}

func (s *Svc) loop() {
	_ = s.db.id
	s.n++
}

func Run() *Svc {
	return New()
}
