package telemetry

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// Windows readers must allow replacement while holding a handle. os.Open's
// default sharing mode would otherwise make ordinary dashboard reads abort a run.
func openStatus(path string) (*os.File, error) {
	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(ptr, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

// Windows can briefly deny replacement even with delete sharing (for example
// while a removed handle is closing). Bound file-only retries to 150 ms.
// This never retries any broker API call.
func publish(source, destination string) error {
	unlock, lockErr := lockFile(destination)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		err = os.Rename(source, destination)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) && !errors.Is(err, syscall.Errno(32)) {
			return err
		}
		if attempt < 4 {
			time.Sleep(time.Duration(10<<attempt) * time.Millisecond)
		}
	}
	return err
}
