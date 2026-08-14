package good_nested_client

// Outer is shared read-only; nested Client is a separately allocated object.
type Client struct { // want Client:"concurrentSafe"
	hits int
}

func (c *Client) Ping() {
	_ = c
}

type Outer struct {
	Name string
	Cli  *Client
}

func Start(o *Outer) { // want Start:"mayShareParams param0:read"
	go func() {
		_ = o.Name
		o.Cli.Ping()
	}()
}
