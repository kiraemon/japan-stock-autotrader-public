// Package instanceguard provides an OS-managed lifetime gate for a connection owner.
package instanceguard

import (
	"errors"
	"sync"
)

var ErrHeld = errors.New("収集プログラムは既に起動しています。終了してから実行してください")

// Guard holds the gate until Close or process termination. It contains no secrets.
type Guard struct {
	once    sync.Once
	release func()
}

func (g *Guard) Close() {
	g.once.Do(g.release)
}
