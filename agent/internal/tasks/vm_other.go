//go:build !linux

package tasks

func freeBytes(string) uint64 { return 0 }
