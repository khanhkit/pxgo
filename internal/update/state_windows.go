//go:build windows

package update

import (
	"golang.org/x/sys/windows"
)

func replaceStateFile(source, target string) error {
	// The source is a temporary file in the destination directory. Never
	// delete the old state before a replacement is ready: failed moves must
	// leave the previous durable state intact.
	return windows.MoveFileEx(
		windows.StringToUTF16Ptr(source),
		windows.StringToUTF16Ptr(target),
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}
