//go:build openbsd || netbsd

package inventory

// Disk enumeration on OpenBSD/NetBSD (getfsstat/statvfs layouts differ) is not
// implemented yet; report that honestly instead of guessing.
func collectBSDDisks(inv *Inventory) {
	inv.warn("disk inventory is not implemented on " + inv.OS.Family + " yet")
}
