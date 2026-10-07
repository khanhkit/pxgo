package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const currentConfigSchema = 1

// SystemProxyImport is a lossless subset of operating-system proxy state that
// can be materialized into pxgo.ini. Dynamic marks WPAD/AutoDetect or other
// routing that must remain owned by the OS resolver rather than flattened.
type SystemProxyImport struct {
	Found   bool
	PAC     string
	Server  string
	NoProxy string
	Dynamic bool
}

// EnsureCanonicalINI creates a documented pxgo.ini on first run, migrates a
// legacy px.ini without modifying it, or incrementally upgrades an existing
// pxgo.ini. Existing user files are never rewritten without a backup.
func EnsureCanonicalINI(loadedPath string, system SystemProxyImport) (string, bool, error) {
	loadedPath = normalizePathIfSet(loadedPath)
	if loadedPath != "" && !isCanonicalOrLegacyINI(loadedPath) {
		return loadedPath, false, nil
	}

	if isLegacyINI(loadedPath) {
		raw, err := os.ReadFile(loadedPath) // #nosec G703 -- resolved config path.
		if err != nil {
			return "", false, err
		}
		path := filepath.Join(filepath.Dir(loadedPath), "pxgo.ini")
		if _, err := os.Stat(path); err == nil {
			return ensureExistingCanonical(path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", false, err
		}
		content := migrateLegacyINI(raw)
		if err := writeNewINI(path, content); err != nil {
			fallback := ConfigPathForSave("")
			if fallback == path {
				return "", false, err
			}
			if fallbackErr := writeNewINI(fallback, content); fallbackErr != nil {
				return "", false, fmt.Errorf("write migrated config next to legacy: %v; fallback %s: %w", err, fallback, fallbackErr)
			}
			return fallback, true, nil
		}
		return path, true, nil
	}

	if loadedPath != "" {
		return ensureExistingCanonical(loadedPath)
	}

	if err := validateSystemProxyImport(system); err != nil {
		return "", false, err
	}
	path := ConfigPathForSave("")
	if _, err := os.Stat(path); err == nil {
		return ensureExistingCanonical(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	if err := writeNewINI(path, []byte(renderFreshINI(system))); err != nil {
		return "", false, err
	}
	return path, true, nil
}

// ApplySystemProxyINI explicitly imports current system routing. It changes
// only proxy/PAC/bypass fields and always backs up an existing canonical INI.
func ApplySystemProxyINI(loadedPath string, system SystemProxyImport) (string, error) {
	if err := validateSystemProxyImport(system); err != nil {
		return "", err
	}
	path := canonicalPathForLoaded(loadedPath)
	if path == "" {
		path = ConfigPathForSave("")
	}

	if isLegacyINI(loadedPath) {
		if _, _, err := EnsureCanonicalINI(loadedPath, SystemProxyImport{}); err != nil {
			return "", err
		}
	}

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) { // #nosec G703 -- resolved config path.
		if err := writeNewINI(path, []byte(renderFreshINI(system))); err != nil {
			return "", err
		}
		return path, nil
	} else if err != nil {
		return "", err
	}
	_, err := mutateFileWithBackup(path, func(raw []byte) ([]byte, error) {
		return []byte(patchSystemRouting(string(raw), system)), nil
	})
	return path, err
}

func ensureExistingCanonical(path string) (string, bool, error) {
	raw, err := os.ReadFile(path) // #nosec G703 -- resolved config path.
	if err != nil {
		return path, false, err
	}
	if configSchema(raw) >= currentConfigSchema {
		return path, false, nil
	}
	changed, err := mutateFileWithBackup(path, func(current []byte) ([]byte, error) {
		if configSchema(current) >= currentConfigSchema {
			return current, nil
		}
		return upgradeToSchema1(current), nil
	})
	return path, changed, err
}

func migrateLegacyINI(raw []byte) []byte {
	body := strings.TrimRight(string(raw), "\r\n")
	if body != "" {
		body += "\n\n"
	}
	body += schemaHeader() + migrationBlock(activeINIKeys(raw))
	return []byte(body)
}

func upgradeToSchema1(raw []byte) []byte {
	body := string(raw)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body = schemaHeader() + "\n" + body
	body += migrationBlock(activeINIKeys(raw))
	return []byte(body)
}

func schemaHeader() string {
	return fmt.Sprintf("# pxgo-config-schema: %d", currentConfigSchema)
}

func migrationBlock(keys map[string]bool) string {
	var b strings.Builder
	b.WriteString("\n# Added by PxGo configuration upgrade. Existing values above are preserved.\n")
	if !keys[keyListen] {
		b.WriteString("# PxGo listens on all interfaces by default. WARNING: combine this with a restrictive allow list or client authentication on untrusted networks.\n")
		b.WriteString("listen = 0.0.0.0\n")
	}
	if !keys[keyAllow] {
		b.WriteString("# Client allow list. *.*.*.* permits every IPv4 client that can reach the listener.\n")
		b.WriteString("allow = *.*.*.*\n")
	}
	if !keys[keyDNS] {
		b.WriteString("\n# Custom DNS / DoH is optional. Leave these commented to use system/VPN DNS.\n")
		b.WriteString("# dns = https://1.1.1.1/dns-query\n")
		b.WriteString("# dns_only = *.google.com\n")
		b.WriteString("# dns_bypass = bosch.com,*.bosch.com\n")
	}
	if !keys[keyAutoUpdate] {
		b.WriteString("\n# Automatic updates: off | notify | install. install is the PxGo default.\n")
		b.WriteString("auto_update = install\n")
	}
	if !keys[keyUpdateInterval] {
		b.WriteString("# Interval between automatic update checks.\nupdate_interval = 24h\n")
	}
	if !keys[keyUpdateChannel] {
		b.WriteString("# Release channel: stable | prerelease.\nupdate_channel = stable\n")
	}
	if !keys[keyInstallProvider] {
		b.WriteString("# Update owner: auto | direct | winget | scoop | homebrew.\ninstall_provider = auto\n")
	}
	return b.String()
}

func renderFreshINI(system SystemProxyImport) string {
	var route strings.Builder
	switch {
	case system.Dynamic:
		route.WriteString("# System proxy uses dynamic WPAD/AutoDetect or protocol-specific routing.\n# server/pac stay unset so PxGo continues using the operating-system resolver.\n# server =\n# pac =\n")
	case system.PAC != "":
		route.WriteString("# Imported from the operating-system proxy configuration. PAC has precedence over a manual HTTP proxy.\n")
		fmt.Fprintf(&route, "pac = %s\n# server =\n", system.PAC)
	case system.Server != "":
		route.WriteString("# Imported from the operating-system proxy configuration.\n")
		fmt.Fprintf(&route, "server = %s\n# pac =\n", system.Server)
	default:
		route.WriteString("# Leave server and pac unset to follow the operating-system proxy configuration, then DIRECT when none exists.\n# server = proxy.company.com:8080\n# pac = http://proxy.company.com/proxy.pac\n")
	}
	if system.NoProxy != "" && !system.Dynamic {
		fmt.Fprintf(&route, "noproxy = %s\n", system.NoProxy)
	} else {
		route.WriteString("# noproxy = localhost,127.0.0.1,*.company.com\n")
	}

	return fmt.Sprintf(`# PxGo configuration
%s
# Source priority: pxgo.ini -> px.ini -> system proxy -> DIRECT.
# Optional features are commented out until explicitly enabled.

[proxy]
%s
# PAC source encoding: auto or an explicit supported encoding.
# pac_encoding = auto

port = 3128
listen = 0.0.0.0

# WARNING: listen=0.0.0.0 exposes PxGo on every reachable interface.
# Restrict allow and/or enable client authentication on untrusted networks.
allow = *.*.*.*

# gateway = 0
# hostonly = 0

# Custom DNS / DNS-over-HTTPS is opt-in. Unset means system/VPN DNS.
# dns = https://1.1.1.1/dns-query
# dns_only = *.google.com
# dns_bypass = bosch.com,*.bosch.com

# Optional upstream request/auth settings.
# useragent =
# username =
# auth =
# kerberos = 0

[client]
# client_auth = NONE
# client_username =
# client_nosspi = 0

[settings]
# workers * threads is the accepted-connection admission budget.
workers = 1
threads = 32
idle = 30
socktimeout = 20.0
proxyreload = 60

# foreground = 0
# log = 0

# Automatic updates are enabled by default. Modes: off | notify | install.
auto_update = install
update_interval = 24h
update_channel = stable
install_provider = auto
`, schemaHeader(), route.String())
}

func validateSystemProxyImport(system SystemProxyImport) error {
	for name, value := range map[string]string{
		keyPAC: system.PAC, keyServer: system.Server, keyNoProxy: system.NoProxy,
	} {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("system proxy %s contains a line break", name)
		}
	}
	return nil
}

func patchSystemRouting(raw string, system SystemProxyImport) string {
	values := map[string]*string{}
	empty := ""
	switch {
	case system.Dynamic || !system.Found:
		values[keyServer] = &empty
		values[keyPAC] = &empty
	case system.PAC != "":
		pac := system.PAC
		values[keyPAC] = &pac
		values[keyServer] = &empty
	case system.Server != "":
		server := system.Server
		values[keyServer] = &server
		values[keyPAC] = &empty
	default:
		values[keyServer] = &empty
		values[keyPAC] = &empty
	}
	noProxy := system.NoProxy
	if system.Dynamic {
		noProxy = ""
	}
	values[keyNoProxy] = &noProxy
	return patchINIKeys(raw, values)
}

func patchINIKeys(raw string, values map[string]*string) string {
	lines := strings.Split(raw, "\n")
	seen := map[string]bool{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "[") {
			continue
		}
		key, _, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		key = canonicalConfigKey(strings.TrimSpace(key))
		value, wanted := values[key]
		if !wanted {
			continue
		}
		if seen[key] || value == nil || *value == "" {
			lines[i] = "# " + strings.TrimSpace(line) + "  # disabled by --apply-system-proxy"
			continue
		}
		lines[i] = key + " = " + *value
		seen[key] = true
	}
	var appendLines []string
	for _, key := range []string{keyPAC, keyServer, keyNoProxy} {
		value := values[key]
		if value != nil && *value != "" && !seen[key] {
			appendLines = append(appendLines, key+" = "+*value)
		}
	}
	if len(appendLines) > 0 {
		text := strings.Join(lines, "\n")
		text = strings.TrimRight(text, "\n") + "\n\n# Applied from operating-system proxy configuration.\n" + strings.Join(appendLines, "\n") + "\n"
		return text
	}
	return strings.Join(lines, "\n")
}

func activeINIKeys(raw []byte) map[string]bool {
	keys := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "[") {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if ok {
			keys[canonicalConfigKey(strings.TrimSpace(key))] = true
		}
	}
	return keys
}

func configSchema(raw []byte) int {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "# pxgo-config-schema:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "# pxgo-config-schema:"))
		n, err := strconv.Atoi(value)
		if err == nil {
			return n
		}
	}
	return 0
}

func writeNewINI(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return withFileLock(path, 0o600, func() error {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("config already exists: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return atomicWriteFile(path, raw, 0o600)
	})
}

func canonicalPathForLoaded(loadedPath string) string {
	loadedPath = normalizePathIfSet(loadedPath)
	if isLegacyINI(loadedPath) {
		return filepath.Join(filepath.Dir(loadedPath), "pxgo.ini")
	}
	return loadedPath
}

func normalizePathIfSet(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return normalizePath(path)
}

func isLegacyINI(path string) bool {
	return strings.EqualFold(filepath.Base(path), "px.ini")
}

func isCanonicalOrLegacyINI(path string) bool {
	base := filepath.Base(path)
	return strings.EqualFold(base, "pxgo.ini") || strings.EqualFold(base, "px.ini")
}
