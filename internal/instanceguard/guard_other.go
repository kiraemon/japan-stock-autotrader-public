//go:build !windows

package instanceguard

import "errors"

// Offline feature tests are portable; broker sessions are Windows-only.
func Acquire(string) (*Guard, error) {
	return nil, errors.New("kabuステーションへの実接続はWindowsで実行してください")
}
