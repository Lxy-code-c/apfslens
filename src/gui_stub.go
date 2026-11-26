//go:build !windows

package main

import "fmt"

func runApp() { fmt.Println("这个程序只能在 Windows 上运行") }
