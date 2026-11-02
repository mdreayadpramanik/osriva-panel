//go:build !linux && !windows && !freebsd && !openbsd && !netbsd && !dragonfly

package inventory

// Platforms without a native collector (e.g. darwin, illumos) report only the
// portable facts gathered in Collect.
func collectPlatform(inv *Inventory) {
	inv.OS.ID = inv.OS.Family
	inv.InitSystem = "unknown"
	inv.warn("no native inventory collector for " + inv.OS.Family + "; only portable facts are reported")
}
