package main

import (
	"fmt"
	"os"
)

// version is overridden at build time with -ldflags.
var version = "0.0.1-dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("ebg", version)
		return
	}
	fmt.Println("ebg: Error Budget Guard")
	fmt.Println("usage: ebg <command> (commands coming in later steps)")
}
