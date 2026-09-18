package winstartup

import (
	"fmt"
	"strings"
)

const RegistryValueName = "PxGo"

type FileExistsFunc func(path string) bool

func BuildRunCommand(executable, pxini string, exists FileExistsFunc) (string, error) {
	if exists == nil {
		return "", fmt.Errorf("startup path validation is required")
	}
	if err := validateStartupPath("executable", executable); err != nil {
		return "", err
	}
	if err := validateStartupPath("config", pxini); err != nil {
		return "", err
	}
	if !exists(executable) {
		return "", fmt.Errorf("cannot find startup executable %s", executable)
	}
	if !exists(pxini) {
		return "", fmt.Errorf("cannot find startup config %s", pxini)
	}
	return quoteWindowsArg(executable) + " " + quoteWindowsArg("--config="+pxini), nil
}

func validateStartupPath(kind, value string) error {
	switch {
	case value == "":
		return fmt.Errorf("empty startup %s path", kind)
	case strings.ContainsRune(value, '\x00'):
		return fmt.Errorf("startup %s path contains NUL", kind)
	default:
		return nil
	}
}

// quoteWindowsArg encodes one Windows command-line argument using the quoting
// rules consumed by CommandLineToArgvW/Go's os/exec: backslashes before quotes
// and trailing backslashes inside quotes are doubled. Always quoting keeps the
// registry value deterministic and prevents whitespace/metacharacters from
// changing argument boundaries.
func quoteWindowsArg(arg string) string {
	var b strings.Builder
	b.Grow(len(arg) + 2)
	b.WriteByte('"')

	backslashes := 0
	flushBackslashes := func(n int) {
		for range n {
			b.WriteByte('\\')
		}
	}

	for _, r := range arg {
		if r == '\\' {
			backslashes++
			continue
		}
		if r == '"' {
			flushBackslashes(backslashes*2 + 1)
			b.WriteByte('"')
			backslashes = 0
			continue
		}
		flushBackslashes(backslashes)
		backslashes = 0
		b.WriteRune(r)
	}
	flushBackslashes(backslashes * 2)
	b.WriteByte('"')
	return b.String()
}
