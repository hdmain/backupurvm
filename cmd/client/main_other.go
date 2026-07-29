//go:build !linux

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "backupurvm client is Linux-only")
	os.Exit(1)
}
