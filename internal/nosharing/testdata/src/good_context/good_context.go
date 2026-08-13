package good_context

import "context"

func run(ctx context.Context) {
	<-ctx.Done()
}

func Start(ctx context.Context) { // want Start:"mayShareParams param0:read"
	go run(ctx)
}

func WithCancelShare() {
	ctx, cancel := context.WithCancel(context.Background())
	go run(ctx)
	go func() {
		<-ctx.Done()
	}()
	cancel()
}
