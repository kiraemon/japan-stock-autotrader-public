package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

var statusKernel = syscall.NewLazyDLL("kernel32.dll")
var statusCreateMutex = statusKernel.NewProc("CreateMutexW")
var statusReleaseMutex = statusKernel.NewProc("ReleaseMutex")

// Exclude status readers during replacement on Windows, including other app
// processes/sessions. This short file lock is unrelated to broker ownership.
// Mutex ownership is thread-bound; retain this OS thread until release.
func lockFile(path string) (func(), error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("状態記録のパスを確認できません")
	}
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(absolute))))
	name, err := syscall.UTF16PtrFromString(`Global\JSA.Status.` + hex.EncodeToString(sum[:16]))
	if err != nil {
		return nil, errors.New("状態記録のロックを準備できません")
	}
	runtime.LockOSThread()
	handle, _, _ := statusCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(name)))
	runtime.KeepAlive(name)
	if handle == 0 {
		runtime.UnlockOSThread()
		return nil, errors.New("状態記録のロックを開けません")
	}
	result, err := syscall.WaitForSingleObject(syscall.Handle(handle), 1000)
	if err != nil || (result != 0 && result != 0x80) {
		syscall.CloseHandle(syscall.Handle(handle))
		runtime.UnlockOSThread()
		return nil, errors.New("状態記録のロックを取得できません")
	}
	return func() {
		statusReleaseMutex.Call(handle)
		syscall.CloseHandle(syscall.Handle(handle))
		runtime.UnlockOSThread()
	}, nil
}
