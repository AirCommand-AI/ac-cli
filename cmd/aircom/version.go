package main

import (
	"fmt"
	"io"
)

var version = "dev"

func writeVersion(output io.Writer) error {
	_, err := fmt.Fprintf(output, "aircom %s\n", version)
	return err
}
