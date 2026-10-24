//go:build !unix

package config

import "os"

// checkInherited has no ownership to check outside Unix.
func checkInherited(os.FileInfo) error { return nil }
