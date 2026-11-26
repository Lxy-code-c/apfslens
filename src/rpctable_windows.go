//go:build windows

package main

import "fmt"

// 统一的 RPC 表：WebView2 绑定与浏览器兜底模式共用同一套实现。
func rpcTable() map[string]func([]string) string {
	get := func(a []string, i int) string {
		if i < len(a) {
			return a[i]
		}
		return ""
	}
	return map[string]func([]string) string{
		"apiScan":             func(a []string) string { return apiScan() },
		"apiDoctor":           func(a []string) string { return apiDoctor() },
		"apiSupport":          func(a []string) string { return apiSupport() },
		"apiApfsDevices":      func(a []string) string { return apiApfsDevices() },
		"apiApfsMounts":       func(a []string) string { return apiApfsMountList() },
		"apiMounts":           func(a []string) string { return apiMounts() },
		"apiElevate":          func(a []string) string { return apiElevate() },
		"apiTask":             func(a []string) string { return apiTask(get(a, 0)) },
		"apiPartTable":        func(a []string) string { return apiPartTable(get(a, 0)) },
		"apiDuMedia":          func(a []string) string { return apiDuMedia(get(a, 0)) },
		"apiDuTree":           func(a []string) string { return apiDuTree(get(a, 0)) },
		"apiApfsVolumes":      func(a []string) string { return apiApfsVolumes(get(a, 0)) },
		"apiApfsTemplateInfo": func(a []string) string { return apiApfsTemplateInfo(get(a, 0)) },
		"apiApfsMountDrop":    func(a []string) string { return apiApfsMountDrop(get(a, 0)) },
		"apiMount":            func(a []string) string { return apiMount(get(a, 0)) },
		"apiUnmount":          func(a []string) string { return apiUnmount(get(a, 0)) },
		"apiOpen":             func(a []string) string { return apiOpen(get(a, 0)) },
		"apiApfsInfo": func(a []string) string {
			return apiApfsInfo(get(a, 0), pUint(get(a, 1)), pUint(get(a, 2)))
		},
		"apiApfsSnapshots": func(a []string) string {
			return apiApfsSnapshots(get(a, 0), pUint(get(a, 1)), pUint(get(a, 2)))
		},
		"apiDuVerify":  func(a []string) string { return apiDuVerify(get(a, 0), get(a, 1), get(a, 2)) },
		"apiPartExec":  func(a []string) string { return apiPartExec(get(a, 0), get(a, 1), get(a, 2)) },
		"apiApfsBigFiles": func(a []string) string {
			return apiApfsBigFiles(get(a, 0), pUint(get(a, 1)), pUint(get(a, 2)), get(a, 3))
		},
		"apiDuSpace": func(a []string) string {
			return apiDuSpace(get(a, 0), pUint(get(a, 1)), pUint(get(a, 2)), get(a, 3))
		},
		"apiApfsMountSet": func(a []string) string {
			return apiApfsMountSet(get(a, 0), get(a, 1), get(a, 2), get(a, 3))
		},
		"apiApfsMount": func(a []string) string {
			return apiApfsMount(get(a, 0), pUint(get(a, 1)), pUint(get(a, 2)), get(a, 3), get(a, 4))
		},
		"apiApfsList": func(a []string) string {
			return apiApfsList(get(a, 0), pUint(get(a, 1)), pUint(get(a, 2)), get(a, 3), get(a, 4))
		},
		"apiApfsExport": func(a []string) string {
			return apiApfsExport(get(a, 0), pUint(get(a, 1)), pUint(get(a, 2)), get(a, 3), get(a, 4))
		},
		"apiApfsDownload": func(a []string) string {
			return apiApfsDownload(get(a, 0), pUint(get(a, 1)), pUint(get(a, 2)), get(a, 3), get(a, 4))
		},
		"apiApfsFileInfo": func(a []string) string {
			return apiApfsFileInfo(get(a, 0), pUint(get(a, 1)), pUint(get(a, 2)), get(a, 3), get(a, 4))
		},
		"apiApfsSearch": func(a []string) string {
			return apiApfsSearch(get(a, 0), pUint(get(a, 1)), pUint(get(a, 2)), get(a, 3), get(a, 4))
		},
		"apiApfsPreview": func(a []string) string {
			return apiApfsPreview(get(a, 0), pUint(get(a, 1)), pUint(get(a, 2)), get(a, 3), get(a, 4))
		},
		"apiApfsDiff": func(a []string) string {
			return apiApfsDiff(get(a, 0), pUint(get(a, 1)), pUint(get(a, 2)), get(a, 3), get(a, 4), get(a, 5))
		},
		"apiFormat": func(a []string) string {
			return apiFormat(get(a, 0), get(a, 1), get(a, 2), get(a, 3))
		},
		"apiApfsApplyTemplate": func(a []string) string {
			return apiApfsApplyTemplate(get(a, 0), get(a, 1), get(a, 2), get(a, 3))
		},
		"apiApfsUnlock": func(a []string) string {
			return apiApfsUnlock(get(a, 0), pUint(get(a, 1)), pUint(get(a, 2)), get(a, 3))
		},
		"apiApfsQuick": func(a []string) string {
			return apiApfsQuick(get(a, 0), get(a, 1), get(a, 2), get(a, 3), get(a, 4))
		},
		"apiPartTag": func(a []string) string {
			return apiPartTag(get(a, 0), get(a, 1), get(a, 2), get(a, 3))
		},
		"apiPartFormatOne": func(a []string) string {
			return apiPartFormatOne(get(a, 0), get(a, 1), get(a, 2), get(a, 3), get(a, 4), get(a, 5))
		},
	}
}

// executeRPC 带 panic 兜底，任何接口异常都变成错误 JSON。
func executeRPC(name string, args []string) (out string) {
	defer func() {
		if r := recover(); r != nil {
			logf("PANIC in rpc %s: %v", name, r)
			out = jerr(fmt.Sprintf("内部错误：%v（详情见 log.txt）", r))
		}
	}()
	fn, ok := rpcTable()[name]
	if !ok {
		return jerr("未知接口：" + name)
	}
	return fn(args)
}
