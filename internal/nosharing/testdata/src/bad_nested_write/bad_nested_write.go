package bad_nested_write

type Client struct {
	hits int
}

func (c *Client) Bump() {
	c.hits++
}

type Outer struct {
	Name string
	Cli  *Client
}

func Start(o *Outer) { // want Start:"mayShareParams param0:write"
	go func() { // want `shared memory .* written without channel transfer`
		o.Name = "x"
		o.Cli.Bump()
	}()
}
