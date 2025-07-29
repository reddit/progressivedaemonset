package main

import (
	"fmt"
	"os"

	"github.com/reddit/progressivedaemonset/internal/controller"
)

func main() {
	if err := controller.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %s\n", err)
		os.Exit(1)
	}
}
