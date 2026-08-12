package sharewrap

import "sharero"

// Wrap re-exports sharero.Start's share Fact.
func Wrap(p *int) { // want Wrap:"mayShareParams param0:read"
	sharero.Start(p)
}
