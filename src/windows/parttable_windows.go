//go:build windows

package main

// parsePartitionTable 打开物理盘句柄拿到扇区大小，再交给平台无关的解析器。
// 用同一个句柄读，避免重复开关设备；winDevice.ReadAt 内部会处理非扇区对齐。
func parsePartitionTable(dev string) ([]rawPart, string, error) {
	d, err := openWinDevice(dev, 0, 0)
	if err != nil {
		return nil, "", err
	}
	defer d.Close()
	return parseAppPartitionTable(d, d.sector)
}
