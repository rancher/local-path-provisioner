package main

import (
	"flag"
	"fmt"
	"os"
	"syscall"
)

var (
	dirMode     string
	path        string
	sizeInBytes string
	action      string
)

const (
	SETUP    = "create"
	TEARDOWN = "delete"
)

func init() {
	flag.StringVar(&path, "p", "", "Absolute path")
	flag.StringVar(&sizeInBytes, "s", "", "Size in bytes")
	flag.StringVar(&dirMode, "m", "", "Dir mode")
	flag.StringVar(&action, "a", "", fmt.Sprintf("Action name. Can be '%s' or '%s'", SETUP, TEARDOWN))
}

func main() {
	flag.Parse()
	if action != SETUP && action != TEARDOWN {
		fmt.Fprintf(os.Stderr, "Incorrect action: %s\n", action)
		os.Exit(1)
	}

	if path == "" {
		fmt.Fprintf(os.Stderr, "Path is empty\n")
		os.Exit(1)
	}

	if path == "/" {
		fmt.Fprintf(os.Stderr, "Path cannot be '/'\n")
		os.Exit(1)
	}

	if action == TEARDOWN {
		err := os.RemoveAll(path)

		if err != nil {
			fmt.Fprintf(os.Stderr, "Cannot remove directory %s: %s\n", path, err)
			os.Exit(1)
		}
		return
	}

	syscall.Umask(0)

	err := os.MkdirAll(path, 0777)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot create directory %s: %s\n", path, err)
		os.Exit(1)
	}
}
