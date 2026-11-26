//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// probePart 是 C 引擎 apfslens_probe.exe 报出来的一个 APFS 分区。
type probePart struct {
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	BS     int    `json:"bs"`
	NXSB   bool   `json:"nxsb"`
}

func probeExePath() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	name := "apfslens_probe.exe"
	if runtime.GOARCH == "386" {
		name = "apfslens_probe-x86.exe"
	}
	p := filepath.Join(filepath.Dir(self), name)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// cProbeAPFS 用独立的 C 引擎读分区表，和 Go 自己的解析互相核对。
// 任何失败都只是返回 error，不影响主流程。
func cProbeAPFS(path string, base int64) ([]probePart, error) {
	exe := probeExePath()
	if exe == "" {
		return nil, fmt.Errorf("缺少 apfslens_probe.exe")
	}
	out, err := runHidden(exe, "probe", path, strconv.FormatInt(base, 10))
	if err != nil {
		return nil, fmt.Errorf("probe 运行失败：%v %s", err, strings.TrimSpace(out))
	}
	var res struct {
		OK   bool        `json:"ok"`
		Err  string      `json:"error"`
		APFS []probePart `json:"apfs"`
	}
	line := strings.TrimSpace(out)
	if i := strings.LastIndex(line, "\n"); i >= 0 {
		line = strings.TrimSpace(line[i+1:])
	}
	if e := json.Unmarshal([]byte(line), &res); e != nil {
		return nil, fmt.Errorf("probe 输出无法解析：%v", e)
	}
	if !res.OK {
		return nil, fmt.Errorf("probe：%s", res.Err)
	}
	return res.APFS, nil
}
