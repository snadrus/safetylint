package good_nolint

func SameLineReason() {
	counter := 0
	go func() { //nolint:safetylint // bla bla bla
		counter++
	}()
	counter++
}

func SameLineNoReason() {
	counter := 0
	go func() { //nolint:safetylint
		counter++
	}()
	counter++
}

func Preceding() {
	counter := 0
	//nolint:safetylint
	go func() {
		counter++
	}()
	counter++
}

func CommaList() {
	counter := 0
	go func() { //nolint:safetylint,other
		counter++
	}()
	counter++
}

func AnalyzerSpecific() {
	counter := 0
	go func() { //nolint:nosharing
		counter++
	}()
	counter++
}

func SpaceAfterSlash() {
	counter := 0
	go func() { // nolint:safetylint
		counter++
	}()
	counter++
}

func AnalyzerThenUmbrella() {
	counter := 0
	go func() { //nolint:nosharing,safetylint
		counter++
	}()
	counter++
}
