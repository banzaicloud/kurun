package main

import (
	"fmt"
	"os"

	"github.com/banzaicloud/kurun/internal/cmd"
)

func main() {
	rootCmd := cmd.NewRootCommand()

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
