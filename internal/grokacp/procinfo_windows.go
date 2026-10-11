//go:build windows

package grokacp

import "os"

func statUID(st os.FileInfo) (int, bool) { return 0, false }
