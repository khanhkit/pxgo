package update

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type AutoMode string

const (
	AutoOff     AutoMode = "off"
	AutoNotify  AutoMode = "notify"
	AutoInstall AutoMode = "install"
)

const ProviderAuto Provider = "auto"

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
	if configured != ProviderAuto {
		if _, err := ParseProvider(string(configured)); err != nil {
			return "", err
		}
		return configured, nil
	}
	if strings.TrimSpace(executable) == "" {
		return "", errors.New("cannot auto-detect update provider without executable path")
	}

	normalizedExecutable := strings.ReplaceAll(executable, "\\", "/")
	path := strings.ToLower(filepath.ToSlash(filepath.Clean(normalizedExecutable)))
	switch strings.ToLower(strings.TrimSpace(goos)) {
	case "windows":
		switch {
		case strings.Contains(path, "/scoop/apps/pxgo/"):
			return ProviderScoop, nil
		case strings.Contains(path, "/microsoft/winget/packages/"), strings.Contains(path, "/winget/packages/"):
			return ProviderWinGet, nil
		}
	case "darwin", "linux":
		if strings.Contains(path, "/cellar/pxgo/") {
			return ProviderBrew, nil
		}
	}
	return ProviderDirect, nil
}
