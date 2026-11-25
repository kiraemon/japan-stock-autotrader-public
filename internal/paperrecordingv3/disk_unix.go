//go:build !windows

package paperrecordingv3

import "syscall"

func FreeBytes(path string) (uint64, error) {
	var s syscall.Statfs_t
	if e := syscall.Statfs(path, &s); e != nil {
		return 0, e
	}
	return s.Bavail * uint64(s.Bsize), nil
}
