//go:build !windows

package update

import "os"

func replaceStateFile(source, target string) error {
	return os.Rename(source, target)
}
