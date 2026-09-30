//go:build !windows

package update

func RunApplyHelper(_ []string) (bool, int) { return false, 0 }
