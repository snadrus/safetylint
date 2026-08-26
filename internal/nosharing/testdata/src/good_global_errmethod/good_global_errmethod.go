package good_global_errmethod

import (
	"errors"
	"fmt"
)

var ErrX = fmt.Errorf("x")

func Run() string {
	go func() {}()
	if errors.Is(ErrX, ErrX) {
		return ErrX.Error()
	}
	return ""
}
