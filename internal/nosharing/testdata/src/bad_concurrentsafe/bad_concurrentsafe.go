package bad_concurrentsafe

type UnsafeBox struct {
	n int
}

func (u *UnsafeBox) Inc() {
	u.n++
}

func Run() {
	u := &UnsafeBox{}
	go u.Inc() // want `shared memory .* written without channel transfer`
	u.Inc()
}
