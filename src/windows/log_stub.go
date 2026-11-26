//go:build !windows

package main

import "log"

func logf(format string, a ...any) { log.Printf(format, a...) }
