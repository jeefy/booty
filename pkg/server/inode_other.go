//go:build !unix

package server

import "os"

func fileInode(os.FileInfo) uint64 { return 0 }
