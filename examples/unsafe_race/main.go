// A program that safetylint rejects: mutable memory shared across goroutines.
package main

import "fmt"

func main() {
	counter := 0
	go func() {
		counter++
	}()
	counter++
	fmt.Println(counter)
}
