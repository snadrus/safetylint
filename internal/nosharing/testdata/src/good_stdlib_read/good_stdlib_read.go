package good_stdlib_read

import (
	"context"
	"errors"
	"io"
)

func ErrorAfter(err error) { // want ErrorAfter:"mayShareParams param0:read"
	go func() {
		_ = err.Error()
		_ = errors.Is(err, io.EOF)
	}()
}

func CtxAfter(ctx context.Context) { // want CtxAfter:"mayShareParams param0:read"
	go func() {
		<-ctx.Done()
	}()
}
