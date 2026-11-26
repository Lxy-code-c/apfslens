package main

import (
	"os"
	"testing"
)

func TestCopyFileToLocal(t *testing.T) {
	f, err := os.Open("/tmp/apfs.img")
	if err != nil {
		t.Skip("no raw image")
	}
	defer f.Close()
	st, _ := f.Stat()
	c, err := openAPFSContainer(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.volumes) == 0 {
		t.Fatal("no volumes")
	}
	v := c.volumes[0]
	dst := t.TempDir() + "/readme.md"
	n, err := copyFileTo(v, "/README.md", 0, "/tmp/apfs.img", 0, dst)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if n != 2677 {
		t.Fatalf("size %d want 2677", n)
	}
	b, _ := os.ReadFile(dst)
	if len(b) != 2677 {
		t.Fatalf("file %d bytes", len(b))
	}
	exts, total, err := v.dataExtents("/README.md", 0)
	t.Logf("extents=%v total=%d err=%v", exts, total, err)
}
