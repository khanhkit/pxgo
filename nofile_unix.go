//go:build !windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func raiseNofileLimitBestEffort() {
	soft, err := raiseNofileLimitWith(nofileOps{
		get: func() (uint64, uint64, error) {
			var limit unix.Rlimit
			if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
				return 0, 0, err
			}
			return limit.Cur, limit.Max, nil
		},
		set: func(soft, hard uint64) error {
			return unix.Setrlimit(unix.RLIMIT_NOFILE, &unix.Rlimit{Cur: soft, Max: hard})
		},
	})
	if err != nil {
		return
	}
	if soft < nofileWarn {
		fmt.Fprintf(os.Stderr, "warning: file-descriptor soft limit is %d; proxy capacity may be constrained\n", soft)
	}
}
