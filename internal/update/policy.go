package update

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

type AutoMode string

const (
	AutoOff     AutoMode = "off"
	AutoNotify  AutoMode = "notify"
	AutoInstall AutoMode = "install"
)

const ProviderAuto Provider = "auto"

const (
	goosDarwin = "darwin"
	goosLinux  = "linux"
)

func ParseAutoMode(value string) (AutoMode, error) {
	mode := AutoMode(strings.ToLower(strings.TrimSpace(value)))
	if mode == "" {
		mode = AutoOff
	}
	switch mode {
	case AutoOff, AutoNotify, AutoInstall:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported auto-update mode %q", value)
	}
}

func ParseChannel(value string) (Channel, error) {
	channel := Channel(strings.ToLower(strings.TrimSpace(value)))
	if channel == "" {
		channel = Stable
	}
	switch channel {
	case Stable, Prerelease:
		return channel, nil
	default:
		return "", fmt.Errorf("unsupported update channel %q", value)
	}
}

func ParseProvider(value string) (Provider, error) {
	provider := Provider(strings.ToLower(strings.TrimSpace(value)))
	if provider == "" {
		provider = ProviderAuto
	}
	switch provider {
	case ProviderAuto, ProviderDirect, ProviderWinGet, ProviderScoop, ProviderBrew:
		return provider, nil
	default:
		return "", fmt.Errorf("unsupported update provider %q", value)
	}
}

func ResolveProvider(configured Provider, executable, goos string) (Provider, error) {
	if configured == "" {
		configured = ProviderAuto
	}
	if _, err := ParseProvider(string(configured)); err != nil {
		return "", err
	}
	if strings.TrimSpace(executable) == "" {
		return "", errors.New("cannot resolve update provider without executable path")
	}

	detected, owned := detectManagedProvider(executable, goos)
	if configured != ProviderAuto {
		if owned {
			if configured != detected {
				return "", fmt.Errorf("configured update provider %q conflicts with executable ownership %q", configured, detected)
			}
			return configured, nil
		}
		if hinted, ambiguous := detectManagedProviderHint(executable, goos); ambiguous && configured != hinted {
			return "", fmt.Errorf("configured update provider %q conflicts with manager-like executable path for %q", configured, hinted)
		}
		return configured, nil
	}
	if owned {
		return detected, nil
	}
	if hinted, ambiguous := detectManagedProviderHint(executable, goos); ambiguous {
		return "", fmt.Errorf("ambiguous update provider for manager-like executable path; configure install_provider=%s", hinted)
	}
	return ProviderDirect, nil
}

// Auto-detection requires both an expected package layout and its known root.
// Custom manager roots require an explicit install_provider or the relevant
// package-manager root environment variable.
var (
	scoopDefaultRoot  = regexp.MustCompile(`^[a-z]:/(?:users/[^/]+|programdata)/scoop$`)
	wingetUserRoot    = regexp.MustCompile(`^[a-z]:/users/[^/]+/appdata/local/microsoft/winget/packages$`)
	wingetMachineRoot = regexp.MustCompile(`^[a-z]:/program files(?: \(x86\))?/winget/packages$`)
	// Hint only when the path has a package-manager install structure, not
	// merely a Windows username or a directory named like a manager.
	brewManagedExecutableHint = regexp.MustCompile(`(?i)/cellar/[^/]+/[^/]+/bin/pxgo$`)
)

func managedPath(value string, caseInsensitive bool) string {
	cleaned := path.Clean(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"))
	if caseInsensitive {
		return strings.ToLower(cleaned)
	}
	return cleaned
}

func managedEnvRoot(key string, caseInsensitive bool) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		// macOS maps /var to /private/var. Compare canonical roots with
		// Service.Check's already-resolved executable path.
		if real, err := filepath.EvalSymlinks(value); err == nil && real != "" {
			value = real
		}
		return managedPath(value, caseInsensitive)
	}
	return ""
}

func underManagedRoot(full, root string) (string, bool) {
	if root == "" || !strings.HasPrefix(full, root+"/") {
		return "", false
	}
	return strings.TrimPrefix(full, root+"/"), true
}

func isScoopOwned(full string) bool {
	const marker = "/apps/pxgo/"
	i := strings.LastIndex(full, marker)
	if i < 0 {
		return false
	}
	root := full[:i]
	if !scoopDefaultRoot.MatchString(root) &&
		root != managedEnvRoot("SCOOP", true) && root != managedEnvRoot("SCOOP_GLOBAL", true) {
		return false
	}
	parts := strings.Split(full[i+len(marker):], "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] == windowsExecutableName
}

func validWingetPackagePath(relative string) bool {
	parts := strings.Split(relative, "/")
	if len(parts) < 2 || len(parts) > 3 || parts[len(parts)-1] != windowsExecutableName {
		return false
	}
	packageDir := parts[0]
	return packageDir == "khanhkit.pxgo" ||
		(strings.HasPrefix(packageDir, "khanhkit.pxgo_") && len(packageDir) > len("khanhkit.pxgo_"))
}

func isWingetOwned(full string) bool {
	for _, root := range []string{
		managedEnvRoot("LOCALAPPDATA", true) + "/microsoft/winget/packages",
		managedEnvRoot("PROGRAMFILES", true) + "/winget/packages",
		managedEnvRoot("PROGRAMFILES(X86)", true) + "/winget/packages",
	} {
		if root == "/microsoft/winget/packages" || root == "/winget/packages" {
			continue
		}
		if remainder, ok := underManagedRoot(full, root); ok && validWingetPackagePath(remainder) {
			return true
		}
	}
	i := strings.LastIndex(full, "/packages/")
	if i < 0 {
		return false
	}
	root := full[:i+len("/packages")]
	if !wingetUserRoot.MatchString(root) && !wingetMachineRoot.MatchString(root) {
		return false
	}
	return validWingetPackagePath(full[i+len("/packages/"):])
}

func isBrewOwned(full string) bool {
	roots := []string{"/opt/homebrew", "/usr/local", "/home/linuxbrew/.linuxbrew"}
	if root := managedEnvRoot("HOMEBREW_PREFIX", false); root != "" {
		roots = append(roots, root)
	}
	if root := managedEnvRoot("HOMEBREW_CELLAR", false); root != "" && strings.HasSuffix(root, "/Cellar") {
		roots = append(roots, strings.TrimSuffix(root, "/Cellar"))
	}
	for _, root := range roots {
		relative, ok := underManagedRoot(full, root+"/Cellar/pxgo")
		if !ok {
			continue
		}
		parts := strings.Split(relative, "/")
		if len(parts) == 3 && parts[0] != "" && parts[1] == "bin" && parts[2] == productName {
			return true
		}
	}
	return false
}

func detectManagedProvider(executable, goos string) (Provider, bool) {
	full := managedPath(executable, strings.EqualFold(goos, goosWindows))
	switch strings.ToLower(strings.TrimSpace(goos)) {
	case goosWindows:
		if isScoopOwned(full) {
			return ProviderScoop, true
		}
		if isWingetOwned(full) {
			return ProviderWinGet, true
		}
	case goosDarwin, goosLinux:
		if isBrewOwned(full) {
			return ProviderBrew, true
		}
	}
	return "", false
}

func detectManagedProviderHint(executable, goos string) (Provider, bool) {
	full := managedPath(executable, strings.EqualFold(goos, goosWindows))
	switch strings.ToLower(strings.TrimSpace(goos)) {
	case goosWindows:
		switch {
		case strings.Contains(full, "/scoop/apps/"), strings.Contains(full, "/scoop/shims/"):
			return ProviderScoop, true
		case strings.Contains(full, "/winget/packages/"), strings.Contains(full, "/winget/links/"):
			return ProviderWinGet, true
		}
	case goosDarwin, goosLinux:
		// A manager-shaped path is ambiguous even if case prevents it from
		// matching the genuine case-sensitive Homebrew root.
		if brewManagedExecutableHint.MatchString(full) {
			return ProviderBrew, true
		}
	}
	return "", false
}
