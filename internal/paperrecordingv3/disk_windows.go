package paperrecordingv3

import (
	"syscall"
	"unsafe"
)

var diskFreeProc = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

func FreeBytes(path string) (uint64, error) {
	p, e := syscall.UTF16PtrFromString(path)
	if e != nil {
		return 0, e
	}
	var available, total, free uint64
	r, _, err := diskFreeProc.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&available)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&free)))
	if r == 0 {
		return 0, err
	}
	return available, nil
}
