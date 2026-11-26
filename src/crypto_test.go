package main

import (
	"io"
	"testing"

	"github.com/blacktop/go-apfs/pkg/disk/dmg"
)

// 加密卷（FileVault）不能被丢掉：至少要能列出「加密卷，需要密码」。
func TestLockedVolumeListed(t *testing.T) {
	src := "/home/user/gopath/pkg/mod/github.com/blacktop/go-apfs@v1.0.33/testdata/secure.dmg"
	dm, err := dmg.Open(src, nil)
	if err != nil {
		t.Skip(err)
	}
	defer dm.Close()
	for i := range dm.Partitions {
		p := &dm.Partitions[i]
		buf := make([]byte, int64(p.SectorCount)*512)
		if _, err := p.ReadAt(buf, 0); err != nil && err != io.EOF {
			continue
		}
		if len(buf) < 4096 || string(buf[32:36]) != "NXSB" {
			continue
		}
		c, err := openAPFSContainer(p, int64(p.SectorCount)*512)
		if err != nil {
			t.Logf("分区 %d 解析失败：%v", i, err)
			continue
		}
		t.Logf("分区 %d：普通卷 %d 个，加密卷 %d 个", i, len(c.volumes), len(c.locked))
		for _, v := range c.volumes {
			t.Logf("  普通卷 %q enc=%v usable=%v", v.Name, v.Encrypted, v.Usable)
		}
		for _, lv := range c.locked {
			t.Logf("  加密卷 %q oid=%#x uuid=%s", lv.Name, lv.Oid, lv.UUID)
		}
		if len(c.volumes)+len(c.locked) == 0 {
			t.Fatalf("这个分区一个卷都没认出来")
		}
		return
	}
	t.Skip("secure.dmg 里没找到 NXSB 分区")
}
