package main

import "github.com/ashutoshsinghai/punch/cmd"

// version is set at build time via goreleaser ldflags.
var version = "dev"

func main() {
	cmd.Version = version
	cmd.Execute()
}
