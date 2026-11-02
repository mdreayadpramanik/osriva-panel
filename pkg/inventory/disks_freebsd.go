//go:build freebsd || dragonfly

package inventory

import "golang.org/x/sys/unix"

var bsdRealFS = map[string]bool{"ufs": true, "zfs": true, "msdosfs": true, "ext2fs": true, "nfs": true}

func collectBSDDisks(inv *Inventory) {
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		inv.warn("getfsstat: " + err.Error())
		return
	}
	buf := make([]unix.Statfs_t, n)
	if _, err := unix.Getfsstat(buf, unix.MNT_NOWAIT); err != nil {
		inv.warn("getfsstat: " + err.Error())
		return
	}
	for _, s := range buf {
		fs := unix.ByteSliceToString(s.Fstypename[:])
		if !bsdRealFS[fs] {
			continue
		}
		bs := uint64(s.Bsize)
		inv.Disks = append(inv.Disks, Disk{
			Mount: unix.ByteSliceToString(s.Mntonname[:]), FSType: fs,
			Device:     unix.ByteSliceToString(s.Mntfromname[:]),
			TotalBytes: uint64(s.Blocks) * bs, FreeBytes: uint64(s.Bavail) * bs,
		})
	}
}
