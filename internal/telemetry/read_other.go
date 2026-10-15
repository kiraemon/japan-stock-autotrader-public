//go:build !windows

package telemetry

import "os"

func openStatus(path string) (*os.File, error) { return os.Open(path) }

func publish(source, destination string) error { return os.Rename(source, destination) }

func lockFile(string) (func(), error) { return func() {}, nil }
