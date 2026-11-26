package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blacktop/go-apfs/types"
)

// ---------------------------------------------------------------------------
// 纯 Go 的 APFS 只读解析层（容器 / 卷 / 目录 / 文件 / 快照）
// 全部只读，不写盘、不联网。
// ---------------------------------------------------------------------------

var apfsGlobalMu sync.Mutex

type apfsContainer struct {
	r        io.ReaderAt
	size     int64
	bs       uint64
	nx       types.NxSuperblock
	omapRoot *types.BTreeNodePhys
	volumes  []*apfsVolume
	locked   []*lockedVol
	blockCnt uint64
	notes    []string
}

type apfsVolume struct {
	c     *apfsContainer
	oid   types.OidT
	paddr uint64
	sb    types.ApfsSuperblock

	omap   *types.BTreeNodePhys
	fsRoot *types.BTreeNodePhys

	Sealed          bool
	Usable          bool
	Encrypted       bool
	Name            string
	Role            string
	UUID            string
	GroupID         string
	Capacity        uint64
	Used            uint64
	Files           uint64
	Dirs            uint64
	Symlinks        uint64
	Others          uint64
	Snaps           uint64
	CaseInsensitive bool
	OpenErr         string

	// inode 信息缓存：同一目录反复浏览时不再重复走 B 树
	inodeCache map[uint64]*inodeBrief

	// 目录列表缓存：APFS 这里只读，同一个目录再点回去不必重新走 B 树。
	// 键是 path+"@"+snap，值带时间戳，超过 10 秒或目录被重建时失效。
	dirCache map[string]*dirCacheEntry

	// inodeCache / dirCache 会被界面线程和后台导出协程同时访问，
	// 没有锁会 concurrent map read/write 直接 panic（这是真实的闪退来源）。
	cacheMu sync.Mutex
}

type dirCacheEntry struct {
	at   time.Time
	ents []apfsDirEntry
	err  string
}

type inodeBrief struct {
	Size      uint64
	Mtime     int64
	Type      string
	Nchildren int32
}

type apfsDirEntry struct {
	Name  string
	Type  string // dir / file / link / other
	Size  uint64
	Mtime int64
	Oid   types.OidT
}

type apfsStat struct {
	Name     string
	Size     uint64
	RawSize  uint64
	Type     string
	Create   int64
	Mod      int64
	Change   int64
	Access   int64
	Owner    uint32
	Group    uint32
	Mode     uint32
	Nlink    uint64
	BsdFlags uint64
	Flag     uint64
	Protect  uint8
	Parent   uint64
	Private  uint64
	Xfields  string
	Oid      types.OidT
}

type lockedVol struct {
	Oid      types.OidT
	Paddr    uint64
	Name     string
	Role     string
	UUID     string
	GroupID  string
	Capacity uint64
	Used     uint64
	Files    uint64
	Dirs     uint64
	Snaps    uint64
}

type inoBrief struct {
	size  uint64
	mtime int64
	ok    bool
}

type apfsSnapshot struct {
	Name    string
	Xid     uint64
	Created int64
	Sblock  uint64
}

func roleName(role uint16) string {
	switch role {
	case 0x0001:
		return "系统卷（System）"
	case 0x0002:
		return "数据卷（Data）"
	case 0x0004:
		return "恢复卷（Recovery）"
	case 0x0008:
		return "VM 卷"
	case 0x0010:
		return "Preboot 卷"
	case 0x0020:
		return "更新卷（Update）"
	case 0x0040:
		return "XART 卷"
	case 0x0080:
		return "硬件卷（Hardware）"
	case 0x0100:
		return "备份卷（Backup）"
	case 0x0200:
		return "企业卷（Enterprise）"
	case 0x0000:
		return "普通卷"
	default:
		return fmt.Sprintf("%#x", role)
	}
}

func openAPFSContainer(r io.ReaderAt, size int64) (*apfsContainer, error) {
	apfsGlobalMu.Lock()
	defer apfsGlobalMu.Unlock()
	return openContainerLocked(r, size)
}

// probeBlockSize 直接读 NXSB 头，拿到 block_size（4K/16K/64K 都有可能）。
func probeBlockSize(r io.ReaderAt) (uint64, error) {
	buf := make([]byte, 4096)
	n, err := r.ReadAt(buf, 0)
	if n < 4096 {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return 0, fmt.Errorf("读取容器超级块失败（只读回 %d 字节）：%v", n, err)
	}
	if string(buf[32:36]) != "NXSB" {
		return 0, fmt.Errorf("块 0 不是 NXSB（分区偏移可能不对，或不是 APFS）")
	}
	bs := uint64(binary.LittleEndian.Uint32(buf[36:40]))
	switch bs {
	case 4096, 8192, 16384, 32768, 65536:
		return bs, nil
	}
	// 少数镜像头里块大小写得不对，按 4096 兜底再试
	return 4096, nil
}

func openContainerLocked(r io.ReaderAt, size int64) (*apfsContainer, error) {
	// 关键：go-apfs 的 types.BLOCK_SIZE 是包级全局量，默认值是 0。
	// 第一次 ReadObj 会按 BLOCK_SIZE 字节去读块；如果还是 0 就只读 0 字节，
	// 校验和必然失败 —— 表现就是「任何 APFS 容器都解析不出来」。
	// 所以先用一次原始读把真实的块大小问出来（NXSB 里 block_size 在偏移 36）。
	bs, perr := probeBlockSize(r)
	if perr != nil {
		return nil, perr
	}
	types.BLOCK_SIZE = bs

	obj, err := types.ReadObj(r, 0)
	if err != nil {
		return nil, fmt.Errorf("读取容器超级块失败：%v", err)
	}
	nx, ok := obj.Body.(types.NxSuperblock)
	if !ok {
		return nil, fmt.Errorf("块 0 不是 NXSB（分区偏移可能不对，或不是 APFS）")
	}
	if nx.BlockSize == 0 {
		return nil, fmt.Errorf("NXSB 块大小为 0")
	}
	types.BLOCK_SIZE = uint64(nx.BlockSize)

	c := &apfsContainer{r: r, size: size, bs: uint64(nx.BlockSize), blockCnt: nx.BlockCount}

	valid := nx
	bestXid := uint64(obj.Hdr.Xid)
	xpBlocks := nx.XpDescBlocks &^ (uint32(1) << 31)
	for i := uint32(0); i < xpBlocks && i < 64; i++ {
		o, err := types.ReadObj(r, nx.XpDescBase+uint64(i))
		if err != nil {
			continue
		}
		if n2, ok := o.Body.(types.NxSuperblock); ok && n2.BlockSize != 0 {
			if uint64(o.Hdr.Xid) >= bestXid {
				bestXid = uint64(o.Hdr.Xid)
				valid = n2
			}
		}
	}
	c.nx = valid

	if valid.OMap != nil {
		if om, ok := valid.OMap.Body.(types.OMap); ok && om.Tree != nil {
			if bt, ok := om.Tree.Body.(types.BTreeNodePhys); ok {
				tmp := bt
				c.omapRoot = &tmp
				_ = c.omapRoot.EnableNodeCache(4096)
			}
		}
	}
	if c.omapRoot == nil {
		return nil, fmt.Errorf("容器 OMAP 树不可用")
	}

	if err := c.loadVolumes(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *apfsContainer) loadVolumes() error {
	var entries []types.OMapNodeEntry
	var omapErrs int
	if err := walkOMapTree(c.r, c.omapRoot, &entries, 0, &omapErrs); err != nil {
		return fmt.Errorf("遍历容器 OMAP 失败：%v", err)
	}
	if omapErrs > 0 {
		c.notes = append(c.notes, fmt.Sprintf("OMAP 子树有 %d 个节点读取失败（卷清单可能不完整）", omapErrs))
	}
	// 容器的 OMAP 是按 (oid, xid) 存的，同一个卷在历史事务里会有多条记录。
	// 直接遍历会把同一个卷重复列出来（还可能是旧版本），所以先按 oid 取
	// 「xid 最大且不超过当前事务」的那一条，再按 oid 顺序处理。
	curXid := uint64(c.nx.NextXid)
	bestXid := map[uint64]uint64{}
	bestPaddr := map[uint64]uint64{}
	for _, e := range entries {
		oid := uint64(e.Key.Oid)
		paddr := uint64(e.Val.Paddr)
		if oid == 0 || paddr == 0 {
			continue
		}
		x := uint64(e.Key.Xid)
		if curXid > 0 && x > curXid {
			continue
		}
		if p, ok := bestXid[oid]; !ok || x >= p {
			bestXid[oid] = x
			bestPaddr[oid] = paddr
		}
	}
	oids := make([]uint64, 0, len(bestPaddr))
	for oid := range bestPaddr {
		oids = append(oids, oid)
	}
	sort.Slice(oids, func(i, j int) bool { return oids[i] < oids[j] })

	seen := map[uint64]bool{}
	for _, oid := range oids {
		paddr := bestPaddr[oid]
		if seen[paddr] {
			continue
		}
		o, err := types.ReadObj(c.r, paddr)
		if err != nil {
			if lv := c.parseLockedVol(uint64(oid), paddr); lv != nil {
				c.locked = append(c.locked, lv)
			}
			continue
		}
		sb, ok := o.Body.(types.ApfsSuperblock)
		if !ok {
			continue
		}
		seen[paddr] = true
		v := &apfsVolume{c: c, oid: types.OidT(oid), paddr: paddr, sb: sb}
		v.Sealed = sb.IncompatibleFeatures&types.APFS_INCOMPAT_SEALED_VOLUME != 0
		nm := string(sb.VolumeName[:])
		if i := strings.IndexByte(nm, 0); i >= 0 {
			nm = nm[:i]
		}
		v.Name = strings.TrimSpace(nm)
		if v.Name == "" {
			v.Name = fmt.Sprintf("卷 %#x", oid)
		}
		v.Role = roleName(uint16(sb.Role))
		v.UUID = uuidString(sb.VolumeUUID)
		v.GroupID = uuidString(sb.VolumeGroupID)
		v.Files = sb.NumFiles
		v.Dirs = sb.NumDirectories
		v.Symlinks = sb.NumSymlinks
		v.Others = sb.NumOtherFsobjects
		v.Snaps = sb.NumSnapshots
		v.Capacity = c.blockCnt * c.bs
		v.Used = sb.FsAllocCount * c.bs
		if v.Used > v.Capacity {
			v.Used = v.Capacity
		}
		v.CaseInsensitive = sb.IncompatibleFeatures&types.APFS_INCOMPAT_CASE_INSENSITIVE != 0
		// APFS_FS_UNENCRYPTED（bit0）没置位就是加密卷。以前这里根本没赋值，
		// 加密标志全靠「解析失败」来猜，所以加密卷会显示成「普通卷但打不开」。
		v.Encrypted = uint64(sb.FsFlags)&0x1 == 0

		if sb.OMap != nil {
			if om, ok := sb.OMap.Body.(types.OMap); ok && om.Tree != nil {
				if bt, ok := om.Tree.Body.(types.BTreeNodePhys); ok {
					tmp := bt
					v.omap = &tmp
					_ = v.omap.EnableNodeCache(4096)
				}
			}
		}
		// 卷能读超级块、但目录树打不开：不要直接丢掉（用户会觉得「卷没了」），
		// 照样列出来并说明原因，点进去会明确报错而不是静默无声。
		if v.omap != nil {
			if entry, err := v.omap.GetOMapEntry(c.r, sb.RootTreeOid, types.XidT(^uint64(0))); err == nil && entry != nil {
				if rootObj, err := types.ReadFSTreeNode(c.r, uint64(entry.Val.Paddr), v.Sealed); err == nil {
					if root, ok := rootObj.Body.(types.BTreeNodePhys); ok {
						root.RootOid = sb.RootTreeOid
						v.fsRoot = &root
						v.Usable = true
					}
				}
			}
		}
		if !v.Usable {
			// 元数据可见、内容打不开：多半是未解锁的加密卷或目录树损坏
			if v.Encrypted {
				v.OpenErr = "该卷已加密，需要输入密码解锁后浏览"
				v.Encrypted = true
			} else {
				v.OpenErr = "目录树不可用（元数据可能损坏，或需要解锁）"
			}
		}
		c.volumes = append(c.volumes, v)
	}
	// 兜底：容器超级块的 FsOids 数组列出了容器里所有的卷 oid。
	// FileVault 卷的超级块是密文，ReadObj 解不出来，上面一定会漏掉它；
	// 这里按「声明过但没解析出来」补成加密卷，用户才能在界面上看到
	// 「2 个卷：1 个可读 + 1 个需要密码」，而不是整个容器像是空的。
	declared := map[uint64]uint64{} // oid -> paddr
	for _, o := range c.nx.FsOids {
		if uint64(o) != 0 {
			declared[uint64(o)] = 0
		}
	}
	for _, e := range entries {
		oid := uint64(e.Key.Oid)
		if _, ok := declared[oid]; ok && declared[oid] == 0 {
			declared[oid] = uint64(e.Val.Paddr)
		}
	}
	for oid, paddr := range declared {
		found := false
		for _, v := range c.volumes {
			if uint64(v.oid) == oid {
				found = true
				break
			}
		}
		for _, lv := range c.locked {
			if uint64(lv.Oid) == oid {
				found = true
				break
			}
		}
		if found {
			continue
		}
		lv := c.parseLockedVol(oid, paddr)
		if lv == nil {
			lv = &lockedVol{
				Oid: types.OidT(oid), Paddr: paddr,
				Name:     fmt.Sprintf("加密卷 %#x（需密码解锁）", oid),
				Capacity: c.blockCnt * c.bs,
			}
		}
		c.locked = append(c.locked, lv)
	}

	sort.Slice(c.volumes, func(i, j int) bool { return c.volumes[i].oid < c.volumes[j].oid })
	return nil
}

func (c *apfsContainer) parseLockedVol(oid uint64, paddr uint64) *lockedVol {
	buf := make([]byte, c.bs)
	if _, err := c.r.ReadAt(buf, int64(paddr*c.bs)); err != nil {
		return nil
	}
	var t types.ApfsSuperblockT
	if err := binary.Read(bytes.NewReader(buf), binary.LittleEndian, &t); err != nil {
		return nil
	}
	if t.Magic.String() != types.APFS_MAGIC {
		return nil
	}
	nm := string(t.VolumeName[:])
	if i := strings.IndexByte(nm, 0); i >= 0 {
		nm = nm[:i]
	}
	nm = strings.TrimSpace(nm)
	if nm == "" {
		nm = fmt.Sprintf("加密卷 %#x", oid)
	}
	cap := c.blockCnt * c.bs
	used := t.FsAllocCount * c.bs
	if used > cap {
		used = cap
	}
	return &lockedVol{
		Oid: types.OidT(oid), Paddr: paddr, Name: nm, Role: roleName(uint16(t.Role)),
		UUID: uuidString(t.VolumeUUID), GroupID: uuidString(t.VolumeGroupID),
		Capacity: cap, Used: used, Files: t.NumFiles, Dirs: t.NumDirectories, Snaps: t.NumSnapshots,
	}
}

func walkOMapTree(r io.ReaderAt, node *types.BTreeNodePhys, out *[]types.OMapNodeEntry, depth int, errs *int) error {
	if node == nil || depth > 16 {
		return nil
	}
	if node.IsLeaf() {
		for _, en := range node.Entries {
			if e, ok := en.(types.OMapNodeEntry); ok {
				*out = append(*out, e)
			}
		}
		return nil
	}
	for _, en := range node.Entries {
		e, ok := en.(types.OMapNodeEntry)
		if !ok {
			continue
		}
		child, err := types.ReadBTreeNode(r, uint64(e.Val.Paddr), types.OBJECT_TYPE_OMAP)
		if err != nil {
			if errs != nil {
				(*errs)++
			}
			continue
		}
		if bt, ok := child.Body.(types.BTreeNodePhys); ok {
			if err := walkOMapTree(r, &bt, out, depth+1, errs); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *apfsContainer) findVolume(oid types.OidT) *apfsVolume {
	for _, v := range c.volumes {
		if v.oid == oid {
			return v
		}
	}
	return nil
}

func (c *apfsContainer) volume(oid types.OidT) (*apfsVolume, error) {
	if v := c.findVolume(oid); v != nil {
		return v, nil
	}
	if len(c.volumes) > 0 && (oid == 0 || oid == types.OidT(^uint64(0))) {
		return c.volumes[0], nil
	}
	return nil, fmt.Errorf("找不到卷 %#x", uint64(oid))
}

func (v *apfsVolume) maxXid(snap uint64) types.XidT {
	if snap != 0 {
		return types.XidT(snap)
	}
	return types.XidT(^uint64(0))
}

// 快照视图：优先使用快照自己的超级块 + 根树
func (v *apfsVolume) view(snap uint64) (*types.BTreeNodePhys, *types.BTreeNodePhys, error) {
	if snap == 0 {
		return v.omap, v.fsRoot, nil
	}
	if v.sb.SnapMetaTreeOid != 0 {
		if snaps, err := v.snapshots(); err == nil {
			for _, s := range snaps {
				if s.Xid == snap && s.Sblock != 0 {
					entry, err := v.omap.GetOMapEntry(v.c.r, types.OidT(s.Sblock), types.XidT(snap))
					if err == nil && entry != nil {
						o, err := types.ReadObj(v.c.r, uint64(entry.Val.Paddr))
						if err == nil {
							if sb, ok := o.Body.(types.ApfsSuperblock); ok && sb.OMap != nil {
								var omapBt *types.BTreeNodePhys
								if om, ok := sb.OMap.Body.(types.OMap); ok && om.Tree != nil {
									if bt, ok := om.Tree.Body.(types.BTreeNodePhys); ok {
										tmp := bt
										omapBt = &tmp
										_ = omapBt.EnableNodeCache(2048)
									}
								}
								if omapBt != nil {
									re, err := omapBt.GetOMapEntry(v.c.r, sb.RootTreeOid, types.XidT(snap))
									if err == nil && re != nil {
										ro, err := types.ReadFSTreeNode(v.c.r, uint64(re.Val.Paddr), v.Sealed)
										if err == nil {
											if bt, ok := ro.Body.(types.BTreeNodePhys); ok {
												return omapBt, &bt, nil
											}
										}
									}
								}
							}
						}
					}
					break
				}
			}
		}
	}
	// 退路：按 xid 过滤
	return v.omap, v.fsRoot, nil
}

func (v *apfsVolume) recordsWith(omap *types.BTreeNodePhys, root *types.BTreeNodePhys, oid types.OidT, snap uint64) (types.FSRecords, error) {
	if omap == nil || root == nil {
		return nil, errors.New("卷树不可用")
	}
	return omap.GetFSRecordsForOid(v.c.r, *root, oid, v.maxXid(snap))
}

func entryType(flags uint16) string {
	switch flags {
	case uint16(types.DT_DIR):
		return "dir"
	case uint16(types.DT_REG):
		return "file"
	case uint16(types.DT_LNK):
		return "link"
	default:
		return "other"
	}
}

func (v *apfsVolume) lookup(path string, snap uint64) (types.OidT, string, error) {
	omap, root, err := v.view(snap)
	if err != nil {
		return 0, "", err
	}
	path = strings.Trim(path, "/")
	if path == "" {
		return types.OidT(types.FSROOT_OID), "", nil
	}
	var parts []string
	for _, p := range strings.Split(path, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return types.OidT(types.FSROOT_OID), "", nil
	}
	var cur = types.OidT(types.FSROOT_OID)
	for i, part := range parts {
		recs, err := v.recordsWith(omap, root, cur, snap)
		if err != nil {
			return 0, "", err
		}
		var found *types.JDrecVal
		for _, r := range recs {
			if r.Hdr.GetType() != types.APFS_TYPE_DIR_REC {
				continue
			}
			k, ok := r.Key.(types.JDrecHashedKeyT)
			if !ok {
				continue
			}
			if matchName(k.Name, part) || matchName(cleanDirName(k.Name), part) {
				val, okv := r.Val.(types.JDrecVal)
				if !okv {
					continue
				}
				found = &val
				break
			}
		}
		if found == nil {
			return 0, "", fmt.Errorf("找不到 %s", part)
		}
		if i == len(parts)-1 {
			return types.OidT(found.FileID), part, nil
		}
		if entryType(uint16(found.Flags)) != "dir" {
			return 0, "", fmt.Errorf("%s 不是目录", part)
		}
		cur = types.OidT(found.FileID)
	}
	return cur, parts[len(parts)-1], nil
}

func (v *apfsVolume) listDir(path string, snap uint64) ([]apfsDirEntry, error) {
	// 命中缓存：直接把上次的结果复制一份返回（界面里反复点进点出同一个
	// 文件夹是最高频的操作，这一层能省掉整棵 B 树的遍历）。
	key := path + "@" + strconv.FormatUint(snap, 10)
	v.cacheMu.Lock()
	if c, ok := v.dirCache[key]; ok && time.Since(c.at) < 10*time.Second {
		if c.err != "" {
			errTxt := c.err
			v.cacheMu.Unlock()
			return nil, errors.New(errTxt)
		}
		ents := c.ents
		v.cacheMu.Unlock()
		out := make([]apfsDirEntry, len(ents))
		copy(out, ents)
		return out, nil
	}
	v.cacheMu.Unlock()
	omap, root, err := v.view(snap)
	if err != nil {
		return nil, err
	}
	dirOid, _, err := v.lookup(path, snap)
	if err != nil {
		return nil, err
	}
	recs, err := v.recordsWith(omap, root, dirOid, snap)
	directN := countDirRecs(recs)
	if err != nil || directN == 0 {
		// 按 oid 直接查不到这个目录的条目（有些盘上树结构/键范围会有差异），
		// 退化成一趟整树扫描，确保「文件夹点进去不是空的」。
		recs = v.scanDirRecords(omap, root, dirOid, snap)
	}
	scanN := countDirRecs(recs)
	var entries []apfsDirEntry
	lookups := 0
	const inodeLookupCap = 400
	for _, r := range recs {
		if r.Hdr.GetType() != types.APFS_TYPE_DIR_REC {
			continue
		}
		k, ok := r.Key.(types.JDrecHashedKeyT)
		if !ok {
			continue
		}
		val, okv := r.Val.(types.JDrecVal)
		if !okv {
			continue
		}
		name := cleanDirName(k.Name)
		// 隐藏项（. 开头）在主列表这里就要挡掉：这条是被缓存的 entries
		// 分支（占绝大多数情况），漏掉一处就等于“隐藏项又冒出来了”。
		if name == "" || name == "." || name == ".." || strings.HasPrefix(name, ".") {
			continue
		}
		e := apfsDirEntry{Name: name, Type: entryType(uint16(val.Flags)), Oid: types.OidT(val.FileID)}
		// 目录不需要大小（界面显示 ─），少一次 B 树查询；
		// 大目录只补前 400 项，避免几千项时列表迟迟不出来。
		if e.Type != "dir" && lookups < inodeLookupCap {
			lookups++
			if ib := v.inodeOf(omap, root, types.OidT(val.FileID), snap); ib != nil {
				e.Size = ib.Size
				e.Mtime = ib.Mtime
				if e.Type == "other" {
					e.Type = ib.Type
				}
			}
		}
		entries = append(entries, e)
	}
	// 自愈：inode 里记的子项数比列出来的多，说明按 oid 的查询只拿到了一部分
	// （用户反馈「根目录只有几个文件夹、点进去是空的」多半就是这种部分结果）。
	// 这时补一趟整树扫描，按 (oid,name) 去重合并。
	// 注意：APFS 的 inode.Nchildren 把 "." 和 ".." 也算在内，所以恒比真实条目
	// 数多 2。旧判断 Nchildren > len(entries) 在**每个**目录上都成立，等于每次
	// 列目录都做一趟整树扫描——这就是「越改越卡」的元凶。
	// 现在只在明显偏少（不足一半）时才补扫，正常目录零开销。
	if ib := v.inodeOf(omap, root, dirOid, snap); ib != nil &&
		int(ib.Nchildren) > len(entries)+2 && len(entries)*2 < int(ib.Nchildren) {
		extra := v.scanDirRecords(omap, root, dirOid, snap)
		seen := map[string]bool{}
		for _, e := range entries {
			seen[e.Name] = true
		}
		for _, r := range extra {
			k, ok := r.Key.(types.JDrecHashedKeyT)
			if !ok {
				continue
			}
			val, okv := r.Val.(types.JDrecVal)
			if !okv {
				continue
			}
			name := cleanDirName(k.Name)
			if name == "" || name == "." || name == ".." || strings.HasPrefix(name, ".") || seen[name] {
				continue
			}
			seen[name] = true
			e := apfsDirEntry{Name: name, Type: entryType(uint16(val.Flags)), Oid: types.OidT(val.FileID)}
			if e.Type != "dir" && lookups < inodeLookupCap {
				lookups++
				if ib2 := v.inodeOf(omap, root, e.Oid, snap); ib2 != nil {
					e.Size, e.Mtime = ib2.Size, ib2.Mtime
					if e.Type == "other" {
						e.Type = ib2.Type
					}
				}
			}
			entries = append(entries, e)
		}
	}

	if len(entries) == 0 && path != "/" {
		// 目录确实是空的，还是有子项但树里查不到？靠 inode 的 children 计数判断，
		// 并把诊断信息抛给界面（原来只弹一个 toast，用户看到的就是「空的」）。
		if ib := v.inodeOf(omap, root, dirOid, snap); ib != nil && ib.Nchildren > 0 {
			return nil, fmt.Errorf("目录 %s 在 inode 里记录有 %d 个子项，但目录树里一条也没查到（按 oid 查到 %d 条、整树扫描 %d 条）。该盘的文件系统树可能有本工具尚未支持的键格式。", path, ib.Nchildren, directN, scanN)
		}
	}
	v.cacheMu.Lock()
	if v.dirCache == nil || len(v.dirCache) > 256 {
		v.dirCache = make(map[string]*dirCacheEntry, 64)
	}
	v.dirCache[key] = &dirCacheEntry{at: time.Now(), ents: entries}
	v.cacheMu.Unlock()
	sort.Slice(entries, func(i, j int) bool {
		di, dj := entries[i].Type == "dir", entries[j].Type == "dir"
		if di != dj {
			return di
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}

// matchName 宽松匹配目录名：精确 → 忽略大小写 → 去掉首尾空白后再比。
// APFS 默认大小写不敏感，而且名字里可能有不可见字符，
// 只做严格相等会让「点进文件夹看到空白」。
func matchName(raw, part string) bool {
	if raw == part {
		return true
	}
	if strings.EqualFold(raw, part) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(raw), strings.TrimSpace(part))
}

func cleanDirName(n string) string {
	var b strings.Builder
	for _, r := range n {
		if r < 0x20 {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func (v *apfsVolume) stat(path string, snap uint64) (*apfsStat, error) {
	omap, root, err := v.view(snap)
	if err != nil {
		return nil, err
	}
	oid, name, err := v.lookup(path, snap)
	if err != nil {
		return nil, err
	}
	recs, err := v.recordsWith(omap, root, oid, snap)
	if err != nil {
		return nil, err
	}
	st := &apfsStat{Name: name, Oid: oid, Type: "other"}
	for _, r := range recs {
		if r.Hdr.GetType() != types.APFS_TYPE_INODE {
			continue
		}
		in, ok := r.Val.(types.JInodeVal)
		if !ok {
			continue
		}
		st.Create = apfsTime(int64(in.CreateTime))
		st.Mod = apfsTime(int64(in.ModTime))
		st.Change = apfsTime(int64(in.ChangeTime))
		st.Access = apfsTime(int64(in.AccessTime))
		st.Owner = uint32(in.Owner)
		st.Group = uint32(in.Group)
		st.Mode = uint32(in.Mode)
		// 目录/普通文件/符号链接靠 inode 的 mode 高位判断。
		// 以前 stat 根本没设 Type，导致「这是目录」的判断永远不成立。
		switch uint16(in.Mode) & 0xF000 {
		case 0x4000:
			st.Type = "dir"
		case 0x8000:
			st.Type = "file"
		case 0xA000:
			st.Type = "link"
		default:
			st.Type = "other"
		}
		st.Nlink = uint64(in.NchildrenOrNlink)
		st.BsdFlags = uint64(in.BsdFlags)
		st.Flag = uint64(in.InternalFlags)
		st.Protect = uint8(in.DefaultProtectionClass)
		st.Parent = uint64(in.ParentID)
		st.Private = in.PrivateID
		st.RawSize = in.UncompressedSize
		var xfs []string
		for _, xf := range in.Xfields {
			switch xf.XType {
			case types.INO_EXT_TYPE_NAME:
				if s, ok := xf.Field.(string); ok {
					st.Name = cleanDirName(s)
				}
			case types.INO_EXT_TYPE_DSTREAM:
				if ds, ok := xf.Field.(types.JDstreamT); ok {
					st.Size = ds.Size
				}
			default:
				xfs = append(xfs, fmt.Sprintf("%v", xf.Field))
			}
		}
		if in.InternalFlags&types.INODE_HAS_UNCOMPRESSED_SIZE != 0 && st.RawSize > 0 {
			st.Size = st.RawSize
		}
	}
	return st, nil
}

func apfsTime(n int64) int64 {
	if n == 0 {
		return 0
	}
	if n > 1e15 {
		return n / 1_000_000_000
	}
	if n > 1e12 {
		return n / 1_000_000
	}
	return n
}

// readFile 把文件内容写入 w
func (v *apfsVolume) readFile(path string, snap uint64, w io.Writer) (int64, error) {
	omap, root, err := v.view(snap)
	if err != nil {
		return 0, err
	}
	oid, _, err := v.lookup(path, snap)
	if err != nil {
		return 0, err
	}
	recs, err := v.recordsWith(omap, root, oid, snap)
	if err != nil {
		return 0, err
	}

	var fexts []types.FileExtent
	var rsrcFexts []types.FileExtent
	var decmpfsHdr *types.DecmpfsDiskHeader
	var fileSize uint64
	var privateID uint64
	compressed := false

	for _, rec := range recs {
		switch rec.Hdr.GetType() {
		case types.APFS_TYPE_INODE:
			in, ok := rec.Val.(types.JInodeVal)
			if !ok {
				continue
			}
			privateID = in.PrivateID
			if in.InternalFlags&types.INODE_HAS_UNCOMPRESSED_SIZE != 0 {
				compressed = true
				if in.UncompressedSize > 0 {
					fileSize = in.UncompressedSize
				}
			}
			for _, xf := range in.Xfields {
				if xf.XType == types.INO_EXT_TYPE_DSTREAM && fileSize == 0 {
					if ds, ok := xf.Field.(types.JDstreamT); ok {
						fileSize = ds.Size
					}
				}
			}
		case types.APFS_TYPE_FILE_EXTENT:
			k, kok := rec.Key.(types.JFileExtentKeyT)
			val, vok := rec.Val.(types.JFileExtentValT)
			if !kok || !vok {
				continue
			}
			fexts = append(fexts, types.FileExtent{Address: k.LogicalAddr, Block: val.PhysBlockNum, Length: val.Length()})
		case types.APFS_TYPE_XATTR:
			k, kok := rec.Key.(types.JXattrKeyT)
			if !kok {
				continue
			}
			switch k.Name {
			case types.XATTR_RESOURCEFORK_EA_NAME:
				xv, okx := rec.Val.(types.JXattrValT)
				if !okx {
					continue
				}
				if xv.Flags.DataStream() {
					if ds, ok := xv.Data.(types.JXattrDstreamT); ok {
						rsrcFexts, _ = v.extentsOf(omap, root, types.OidT(ds.XattrObjID), snap)
					}
				}
			case types.XATTR_DECMPFS_EA_NAME:
				if hdr, err := types.GetDecmpfsHeader(rec); err == nil {
					decmpfsHdr = hdr
				}
			}
		}
	}

	if !compressed && len(fexts) == 0 && fileSize > 0 {
		fexts, _ = v.extentsOf(omap, root, types.OidT(privateID), snap)
	}

	if decmpfsHdr != nil {
		// 注意：bufio.Writer.Flush 之后 Buffered() 一定是 0，
		// 以前用它算大小会得到 0 字节，导出的文件是空的。
		cw := &countWriter{w: w}
		bw := bufio.NewWriter(cw)
		if err := decmpfsHdr.DecompressFile(v.c.r, bw, rsrcFexts, true); err != nil {
			return cw.n, fmt.Errorf("解压失败：%v", err)
		}
		if err := bw.Flush(); err != nil {
			return cw.n, err
		}
		return cw.n, nil
	}

	return copyExtents(w, v.c.r, v.c.bs, int64(fileSize), fexts)

}

// copyExtents 按「逻辑偏移」把数据段拼成文件内容（空洞补零、重叠跳过）。
// 独立成函数是为了能被单测直接验证（稀疏文件/碎片文件最容易读错）。
func copyExtents(w io.Writer, r io.ReaderAt, bs uint64, size int64, fexts []types.FileExtent) (int64, error) {
	remaining := size
	var pos int64
	for _, fext := range fexts {
		if remaining <= 0 {
			break
		}
		start := int64(fext.Address)
		length := int64(fext.Length)
		if length <= 0 {
			continue
		}
		if start+length <= pos {
			continue
		}
		skip := int64(0)
		if start < pos {
			skip = pos - start
		}
		if start > pos {
			gap := start - pos
			if gap > remaining {
				gap = remaining
			}
			if err := writeZeros(w, gap); err != nil {
				return pos, err
			}
			pos += gap
			remaining -= gap
			if remaining <= 0 {
				break
			}
		}
		n := length - skip
		if n > remaining {
			n = remaining
		}
		sr := io.NewSectionReader(r, int64(fext.Block*bs)+skip, n)
		m, err := io.Copy(w, io.LimitReader(sr, n))
		pos += m
		remaining -= m
		if err != nil {
			return pos, err
		}
		if m < n {
			break
		}
	}
	return pos, nil
}

// countWriter 统计真实写出的字节数：解压路径必须靠它算大小，
// 不能用 bufio.Writer.Buffered()（Flush 之后必然是 0）。
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func writeZeros(w io.Writer, n int64) error {
	zero := make([]byte, 32*1024)
	for n > 0 {
		c := int64(len(zero))
		if c > n {
			c = n
		}
		m, err := w.Write(zero[:c])
		n -= int64(m)
		if err != nil {
			return err
		}
		if m == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (v *apfsVolume) extentsOf(omap *types.BTreeNodePhys, root *types.BTreeNodePhys, oid types.OidT, snap uint64) ([]types.FileExtent, error) {
	recs, err := v.recordsWith(omap, root, oid, snap)
	if err != nil {
		return nil, err
	}
	var out []types.FileExtent
	for _, r := range recs {
		if r.Hdr.GetType() == types.APFS_TYPE_FILE_EXTENT {
			k, kok := r.Key.(types.JFileExtentKeyT)
			val, vok := r.Val.(types.JFileExtentValT)
			if !kok || !vok {
				continue
			}
			out = append(out, types.FileExtent{Address: k.LogicalAddr, Block: val.PhysBlockNum, Length: val.Length()})
		}
	}
	return out, nil
}

// snapshots 读取快照元数据树
func (v *apfsVolume) snapshots() ([]apfsSnapshot, error) {
	if v.sb.SnapMetaTreeOid == 0 {
		return nil, nil
	}
	paddr := uint64(v.sb.SnapMetaTreeOid)
	if e, err := v.omap.GetOMapEntry(v.c.r, v.sb.SnapMetaTreeOid, types.XidT(^uint64(0))); err == nil && e != nil && uint64(e.Val.Paddr) != 0 {
		paddr = uint64(e.Val.Paddr)
	}
	obj, err := types.ReadBTreeNode(v.c.r, paddr, types.OBJECT_TYPE_SNAPMETATREE)
	if err != nil {
		return nil, fmt.Errorf("读取快照元数据树失败：%v", err)
	}
	bt, ok := obj.Body.(types.BTreeNodePhys)
	if !ok {
		return nil, errors.New("快照元数据树格式异常")
	}
	var out []apfsSnapshot
	seen := map[uint64]bool{}
	var walk func(n *types.BTreeNodePhys, depth int)
	walk = func(n *types.BTreeNodePhys, depth int) {
		if n == nil || depth > 16 {
			return
		}
		for _, en := range n.Entries {
			ne, ok := en.(types.NodeEntry)
			if !ok {
				continue
			}
			if ne.Hdr.GetType() != types.APFS_TYPE_SNAP_METADATA {
				continue
			}
			if n.IsLeaf() {
				xid := ne.Hdr.GetID()
				if seen[xid] {
					continue
				}
				seen[xid] = true
				out = append(out, apfsSnapshot{
					Name:    reflectString(ne.Val, "Name"),
					Xid:     xid,
					Created: apfsTime(reflectInt(ne.Val, "CreateTime")),
					Sblock:  reflectUint(ne.Val, "SblockOid"),
				})
			} else if ne.PAddr != 0 {
				child, err := types.ReadBTreeNode(v.c.r, ne.PAddr, types.OBJECT_TYPE_SNAPMETATREE)
				if err != nil {
					continue
				}
				if cb, ok := child.Body.(types.BTreeNodePhys); ok {
					walk(&cb, depth+1)
				}
			}
		}
	}
	walk(&bt, 0)
	sort.Slice(out, func(i, j int) bool { return out[i].Xid > out[j].Xid })
	return out, nil
}

func reflectField(v any, name string) reflect.Value {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	// 支持匿名嵌入：逐层查找
	var search func(reflect.Value) reflect.Value
	search = func(sv reflect.Value) reflect.Value {
		f := sv.FieldByName(name)
		if f.IsValid() {
			return f
		}
		for i := 0; i < sv.NumField(); i++ {
			sf := sv.Field(i)
			if sf.Kind() == reflect.Struct && sv.Type().Field(i).Anonymous {
				if r := search(sf); r.IsValid() {
					return r
				}
			}
		}
		return reflect.Value{}
	}
	return search(rv)
}

func reflectString(v any, name string) string {
	f := reflectField(v, name)
	if !f.IsValid() {
		return ""
	}
	if f.Kind() == reflect.String {
		return f.String()
	}
	return fmt.Sprintf("%v", f)
}

func reflectInt(v any, name string) int64 {
	f := reflectField(v, name)
	if !f.IsValid() {
		return 0
	}
	switch f.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return f.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(f.Uint())
	case reflect.Struct:
		return reflectInt(f.Interface(), name)
	}
	return 0
}

func reflectUint(v any, name string) uint64 {
	f := reflectField(v, name)
	if !f.IsValid() {
		return 0
	}
	switch f.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return f.Uint()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uint64(f.Int())
	}
	return 0
}

func uuidString(u types.UUID) string {
	var b []byte
	rv := reflect.ValueOf(u)
	if rv.Kind() == reflect.Array {
		for i := 0; i < rv.Len(); i++ {
			b = append(b, byte(rv.Index(i).Uint()))
		}
	}
	if len(b) == 0 {
		return ""
	}
	h := strings.ToLower(hex.EncodeToString(b))
	if len(h) != 32 {
		return h
	}
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func countDirRecs(recs types.FSRecords) int {
	n := 0
	for _, r := range recs {
		if r.Hdr.GetType() == types.APFS_TYPE_DIR_REC {
			n++
		}
	}
	return n
}

func modeType(m uint16) string {
	switch m & 0xF000 {
	case 0x4000:
		return "dir"
	case 0x8000:
		return "file"
	case 0xA000:
		return "link"
	}
	return "other"
}

// inodeOf 取一个 oid 的 inode 摘要（大小/时间/类型），带缓存。
func (v *apfsVolume) inodeOf(omap, root *types.BTreeNodePhys, oid types.OidT, snap uint64) *inodeBrief {
	key := uint64(oid)
	v.cacheMu.Lock()
	if ib, ok := v.inodeCache[key]; ok {
		v.cacheMu.Unlock()
		return ib
	}
	v.cacheMu.Unlock()
	recs, err := v.recordsWith(omap, root, oid, snap)
	if err != nil {
		return nil
	}
	ib := &inodeBrief{Type: "other"}
	for _, r := range recs {
		if r.Hdr.GetType() != types.APFS_TYPE_INODE {
			continue
		}
		in, ok := r.Val.(types.JInodeVal)
		if !ok {
			continue
		}
		ib.Mtime = apfsTime(int64(in.ModTime))
		ib.Type = modeType(uint16(in.Mode))
		ib.Nchildren = in.NchildrenOrNlink
		for _, xf := range in.Xfields {
			if xf.XType == types.INO_EXT_TYPE_DSTREAM {
				if ds, ok := xf.Field.(types.JDstreamT); ok {
					ib.Size = ds.Size
				}
			}
		}
		if in.InternalFlags&types.INODE_HAS_UNCOMPRESSED_SIZE != 0 && in.UncompressedSize > 0 {
			ib.Size = in.UncompressedSize
		}
		break
	}
	v.cacheMu.Lock()
	if v.inodeCache == nil {
		v.inodeCache = map[uint64]*inodeBrief{}
	}
	if len(v.inodeCache) > 4096 { // 防无限增长：大卷遍历几万个文件时不清会吃内存
		v.inodeCache = map[uint64]*inodeBrief{}
	}
	v.inodeCache[key] = ib
	v.cacheMu.Unlock()
	return ib
}

// scanDirRecords 整树扫一遍，收集 key.oid == dirOid 的目录条目。
// 只在「按 oid 查不到」时用，正常情况下不会走到这里。
// 目的是保证任何布局的盘，点进文件夹都能列出内容，而不是空白。
func (v *apfsVolume) scanDirRecords(omap, root *types.BTreeNodePhys, dirOid types.OidT, snap uint64) types.FSRecords {
	var out types.FSRecords
	var walk func(node *types.BTreeNodePhys, depth int)
	visited := map[uint64]bool{}
	nodes := 0
	walk = func(node *types.BTreeNodePhys, depth int) {
		if node == nil || depth > 16 || nodes > 200000 {
			return
		}
		nodes++
		if node.IsLeaf() {
			for _, en := range node.Entries {
				ne, ok := en.(types.NodeEntry)
				if !ok {
					continue
				}
				if ne.Hdr.GetType() != types.APFS_TYPE_DIR_REC {
					continue
				}
				if ne.Hdr.GetID() != uint64(dirOid) {
					continue
				}
				out = append(out, ne)
			}
			return
		}
		for _, en := range node.Entries {
			ne, ok := en.(types.NodeEntry)
			if !ok || ne.PAddr == 0 || visited[ne.PAddr] {
				continue
			}
			visited[ne.PAddr] = true
			obj, err := types.ReadFSTreeNode(v.c.r, ne.PAddr, v.Sealed)
			if err != nil {
				continue
			}
			if child, ok := obj.Body.(types.BTreeNodePhys); ok {
				walk(&child, depth+1)
			}
		}
	}
	walk(root, 0)
	return out
}
