package good_retry_slice

import "errors"

var errorTypes = []error{errors.New("a"), errors.New("b")}

func Retry[T any](errs []error, f func() (T, error)) (T, error) {
	var zero T
	_, _ = errs[0], f
	return zero, nil
}

func Call() error {
	_, err := Retry(errorTypes, func() (int, error) { return 1, nil })
	return err
}

func Run() {
	go func() { _ = Call() }()
	_ = Call()
}
