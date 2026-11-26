package main

import (
	"log"
	"os"
)

func init() {
	// 图形版不把日志打到终端：写到用户目录下的文件里，出问题时可回看。
	_ = os.MkdirAll(logDir(), 0o755)
	f, err := os.OpenFile(logPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		log.SetOutput(f)
	}
}

func logf(format string, a ...any) { log.Printf(format, a...) }

func logDir() string {
	h, _ := os.UserHomeDir()
	return h + "/.u-apfs"
}

func logPath() string { return logDir() + "/log.txt" }
