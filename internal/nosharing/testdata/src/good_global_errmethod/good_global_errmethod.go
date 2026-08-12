package good_global_errmethod

import "fmt"

var ErrX = fmt.Errorf("x")

func Run() string {
	go func() {}()
	return ErrX.Error()
}
