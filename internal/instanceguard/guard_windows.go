package instanceguard

import (
	"errors"
	"runtime"
	"syscall"
	"unsafe"
)

var createMutex = syscall.NewLazyDLL("kernel32.dll").NewProc("CreateMutexW")

// Acquire uses the existence of a named kernel object as a lifetime gate.
// We never own/wait on the mutex, so no OS-thread ownership is involved.
// Every contender closes its handle immediately; the owner retains its handle.
func Acquire(name string) (*Guard, error) {
	ptr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, errors.New("起動ロック名が不正です")
	}
	handle, _, callErr := createMutex.Call(0, 0, uintptr(unsafe.Pointer(ptr)))
	runtime.KeepAlive(ptr)
	if handle == 0 {
		return nil, errors.New("起動ロックを取得できません。別セッションの起動状態と権限を確認してください")
	}
	if callErr == syscall.ERROR_ALREADY_EXISTS {
		syscall.CloseHandle(syscall.Handle(handle))
		return nil, ErrHeld
	}
	return &Guard{release: func() { syscall.CloseHandle(syscall.Handle(handle)) }}, nil
}
