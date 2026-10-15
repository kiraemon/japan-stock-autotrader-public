package credentials

import (
	"errors"
	"runtime"
	"syscall"
	"unsafe"
)

type dataBlob struct {
	size uint32
	data *byte
}

var cryptUnprotect = syscall.NewLazyDLL("crypt32.dll").NewProc("CryptUnprotectData")

func unprotect(encrypted []byte) ([]byte, error) {
	if len(encrypted) == 0 || len(encrypted) > 65536 {
		return nil, errors.New("invalid protected data")
	}
	input := dataBlob{size: uint32(len(encrypted)), data: &encrypted[0]}
	var output dataBlob
	// CRYPTPROTECT_UI_FORBIDDEN: fail instead of showing an interactive DPAPI UI.
	ok, _, _ := cryptUnprotect.Call(uintptr(unsafe.Pointer(&input)), 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(&output)))
	runtime.KeepAlive(encrypted)
	if ok == 0 {
		return nil, errors.New("DPAPI decryption failed")
	}
	defer syscall.LocalFree(syscall.Handle(unsafe.Pointer(output.data)))
	if output.size == 0 || output.size > 65536 || output.data == nil {
		return nil, errors.New("invalid decrypted size")
	}
	plain := unsafe.Slice(output.data, int(output.size))
	defer clear(plain)
	if len(plain) > 16384 {
		return nil, errors.New("decrypted token too large")
	}
	return append([]byte(nil), plain...), nil
}
