package winstartup

import (
	"fmt"
	"path/filepath"
	"strings"
)

const BackgroundExecutableName = "pxgow.exe"

// BackgroundExecutable returns the packaged windowless companion next to the
// console executable. Windows-style paths are handled deterministically even
// when unit tests run on another host OS.
func BackgroundExecutable(consoleExecutable string) (string, error) {
	if err := validateStartupPath("executable", consoleExecutable); err != nil {
		return "", err
	}
	if strings.EqualFold(baseName(consoleExecutable), BackgroundExecutableName) {
		return consoleExecutable, nil
	}
	dir := dirName(consoleExecutable)
	if dir == "" || dir == "." {
		return BackgroundExecutableName, nil
	}
	sep := string(filepath.Separator)
	if strings.Contains(consoleExecutable, `\`) && !strings.Contains(consoleExecutable, `/`) {
		sep = `\`
	}
	return strings.TrimRight(dir, `/\`) + sep + BackgroundExecutableName, nil
}

func baseName(path string) string {
	path = strings.TrimRight(path, `/\`)
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

func dirName(path string) string {
	path = strings.TrimRight(path, `/\`)
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[:i]
	}
	return filepath.Dir(path)
}

// PrepareBackgroundRunCommand persists the effective configuration first and
// then registers the release-owned GUI-subsystem companion for logon startup.
func PrepareBackgroundRunCommand(consoleExecutable, pxini string, exists FileExistsFunc, save SaveConfigFunc) (string, error) {
	backgroundExecutable, err := BackgroundExecutable(consoleExecutable)
	if err != nil {
		return "", err
	}
	if exists == nil {
		return "", fmt.Errorf("startup path validation is required")
	}
	return PrepareRunCommand(backgroundExecutable, pxini, exists, save)
}
