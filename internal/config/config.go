package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

const (
	LogNone = iota
	LogScriptDir
	LogCWD
	LogUniqLog
	LogStdout
)

const LogStdoutTarget = "<stdout>"

const goosWindows = "windows"

const (
	envPrefix = "PXGO_"

	keyServer         = "server"
	keyPAC            = "pac"
	keyPACEncoding    = "pac_encoding"
	keyPort           = "port"
	keyListen         = "listen"
	keyGateway        = "gateway"
	keyHostonly       = "hostonly"
	keyAllow          = "allow"
	keyNoProxy        = "noproxy"
	keyUserAgent      = "useragent"
	keyUsername       = "username"
	keyPassword       = "password"
	keyAuth           = "auth"
	keyKerberos       = "kerberos"
	keyWorkers        = "workers"
	keyThreads        = "threads"
	keyIdle           = "idle"
	keySockTimeout    = "socktimeout"
	keyProxyReload    = "proxyreload"
	keyForeground     = "foreground"
	keyLog            = "log"
	keyClientAuth     = "client_auth"
	keyClientNoSSPI   = "client_nosspi"
	keyClientUsername = "client_username"
	keyClientPassword = "client_password"
	keyConfig         = "config"
	keyTest           = "test"
	keyDotenv         = "dotenv"
	keySave           = "save"
	localhostIP       = "127.0.0.1"
)

const maxConfigLineBytes = 1 << 20

var Defaults = map[string]string{
	keyServer:         "",
	keyPAC:            "",
	keyPACEncoding:    "utf-8",
	keyPort:           "3128",
	keyListen:         localhostIP,
	keyGateway:        "0",
	keyHostonly:       "0",
	keyAllow:          "*.*.*.*",
	keyNoProxy:        "",
	keyUserAgent:      "",
	keyUsername:       "",
	keyAuth:           "",
	keyKerberos:       "0",
	keyWorkers:        "1",
	keyThreads:        "32",
	keyIdle:           "30",
	keySockTimeout:    "20.0",
	keyProxyReload:    "60",
	keyForeground:     "0",
	keyLog:            "0",
	keyClientAuth:     "NONE",
	keyClientNoSSPI:   "0",
	keyClientUsername: "",
}

var (
	executablePath = os.Executable
	userHomeDir    = os.UserHomeDir
)

const (
	Realm       = "pxgo"
	ClientRealm = "pxgo-client"
)

type Config struct {
	Server               string
	PAC                  string
	PACEncoding          string
	Port                 int
	Listen               string
	Gateway              bool
	Hostonly             bool
	Allow                string
	NoProxy              string
	UserAgent            string
	Username             string
	Password             string
	Auth                 string
	Kerberos             bool
	Workers              int
	Threads              int
	Idle                 int
	SockTimeout          float64
	ProxyReload          int
	Foreground           bool
	Log                  int
	Test                 string
	TestAuth             bool
	PasswordAction       bool
	ClientPasswordAction bool
	Help                 bool
	Version              bool
	Install              bool
	Uninstall            bool
	Force                bool
	ConfigPath           string
	Save                 bool
	Quit                 bool
	Restart              bool
	ClientAuth           string
	ClientUsername       string
	ClientPassword       string
	ClientNoSSPI         bool
	Sources              map[string]string
}

func (c Config) SourceOf(name string) string {
	if c.Sources == nil {
		return ""
	}
	return c.Sources[canonicalConfigKey(name)]
}

func Default() Config {
	port, _ := strconv.Atoi(Defaults[keyPort])
	workers, _ := strconv.Atoi(Defaults[keyWorkers])
	threads, _ := strconv.Atoi(Defaults[keyThreads])
	idle, _ := strconv.Atoi(Defaults[keyIdle])
	sockTimeout, _ := strconv.ParseFloat(Defaults[keySockTimeout], 64)
	proxyReload, _ := strconv.Atoi(Defaults[keyProxyReload])
	sources := make(map[string]string, len(Defaults))
	for key := range Defaults {
		sources[key] = "default"
	}
	return Config{
		PACEncoding: Defaults[keyPACEncoding],
		Port:        port,
		Listen:      Defaults[keyListen],
		Allow:       Defaults[keyAllow],
		Workers:     workers,
		Threads:     threads,
		Idle:        idle,
		SockTimeout: sockTimeout,
		ProxyReload: proxyReload,
		Auth:        Defaults[keyAuth],
		ClientAuth:  Defaults[keyClientAuth],
		Sources:     sources,
	}
}

func GetConfigDir() string {
	dir, _ := getConfigDirStrict()
	return dir
}

func getConfigDirStrict() (string, error) {
	if runtime.GOOS == goosWindows {
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "pxgo"), nil
		}
		profile := os.Getenv("USERPROFILE")
		if profile == "" {
			return "", fmt.Errorf("USERPROFILE is empty")
		}
		return filepath.Join(profile, "AppData", "Roaming", "pxgo"), nil
	}
	if runtime.GOOS == "darwin" {
		home, err := userHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "pxgo"), nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "pxgo"), nil
	}
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "pxgo"), nil
}

func GetLogfile(location int) string {
	switch location {
	case LogScriptDir:
		return filepath.Join(GetScriptDir(), "debug-main.log")
	case LogCWD:
		cwd, err := os.Getwd()
		if err != nil {
			return "debug-main.log"
		}
		return filepath.Join(cwd, "debug-main.log")
	case LogUniqLog:
		cwd, err := os.Getwd()
		if err != nil {
			cwd = "."
		}
		name := "main"
		for _, arg := range os.Args {
			if port, ok := strings.CutPrefix(arg, "--port="); ok {
				name = port + "-" + name
				break
			}
		}
		return filepath.Join(cwd, fmt.Sprintf("debug-%s-%d.log", name, time.Now().UnixNano()))
	case LogStdout:
		return LogStdoutTarget
	default:
		return ""
	}
}

func FileURLToLocalPath(fileURL string) string {
	path, err := fileURLToLocalPathStrict(fileURL)
	if err != nil {
		return fileURL
	}
	return path
}

func fileURLToLocalPathStrict(fileURL string) (string, error) {
	normalized := strings.ReplaceAll(fileURL, "\\", "/")
	u, err := url.Parse(normalized)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("expected file URL")
	}
	path, err := url.PathUnescape(u.Path)
	if err != nil {
		return "", err
	}
	var result string
	switch {
	case u.Host != "":
		result = "//" + u.Host + path
	case len(path) >= 3 && path[0] == '/' && path[2] == ':':
		result = path[1:]
	default:
		result = path
	}
	if result == "" {
		return "", fmt.Errorf("empty file path")
	}
	return filepath.FromSlash(result), nil
}

func GetHostIPs() []net.IP {
	seen := map[string]bool{}
	var ips []net.IP
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 {
				continue
			}
			addrs, _ := iface.Addrs()
			for _, addr := range addrs {
				var ip net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip4 := ip.To4(); ip4 != nil && !seen[ip4.String()] {
					seen[ip4.String()] = true
					ips = append(ips, ip4)
				}
			}
		}
	}
	if !seen[localhostIP] {
		ips = append(ips, net.ParseIP(localhostIP))
	}
	return ips
}

func ParseArgs(args []string) (Config, error) {
	cfg := Default()
	dotenv, dotenvSource, err := loadDotenv()
	if err != nil {
		return cfg, err
	}

	isSave := false
	if raw, ok := dotenv[keySave]; ok {
		value, err := parseBoolValue(raw)
		if err != nil {
			return cfg, fmt.Errorf("%s %s: %w", dotenvSource, keySave, err)
		}
		isSave = value
	}
	if raw, ok := os.LookupEnv(envPrefix + "SAVE"); ok {
		value, err := parseBoolValue(raw)
		if err != nil {
			return cfg, fmt.Errorf("environment %sSAVE: %w", envPrefix, err)
		}
		isSave = value
	}
	if hasBareArg(args, keySave) {
		isSave = true
	}
	cfg.Save = isSave

	configPath := preScanConfigPath(args)
	configPathSource := ""
	if configPath != "" {
		configPathSource = "cli"
	} else if raw, ok := os.LookupEnv(envPrefix + "CONFIG"); ok {
		configPath = raw
		configPathSource = "env:" + envPrefix + "CONFIG"
	} else if raw, ok := dotenv[keyConfig]; ok {
		configPath = raw
		configPathSource = dotenvSource
	}
	if configPath != "" {
		cfg.ConfigPath = normalizePath(configPath)
		markSource(&cfg, keyConfig, configPathSource)
	}
	loadPath, err := configPathStrict(configPath)
	if err != nil {
		return cfg, fmt.Errorf("resolve config path: %w", err)
	}
	if loadPath != "" {
		_, statErr := os.Stat(loadPath) // #nosec G703 -- config paths are explicitly user-controlled inputs.
		switch {
		case statErr == nil:
			fileCfg, err := ReadINI(loadPath)
			if err != nil {
				return cfg, fmt.Errorf("read config %s: %w", loadPath, err)
			}
			cfg = fileCfg
			cfg.ConfigPath = loadPath
			cfg.Save = isSave
			if configPathSource != "" {
				markSource(&cfg, keyConfig, configPathSource)
			}
		case configPath != "" && !isSave:
			return cfg, fmt.Errorf("could not open config file %s: %w", loadPath, statErr)
		case configPath == "" && !errors.Is(statErr, os.ErrNotExist):
			return cfg, fmt.Errorf("could not inspect config file %s: %w", loadPath, statErr)
		}
	}
	if err := applyMap(&cfg, dotenv, dotenvSource); err != nil {
		return cfg, err
	}
	if err := applyEnv(&cfg); err != nil {
		return cfg, err
	}
	for _, arg := range args {
		if arg == "--save" {
			cfg.Save = true
			continue
		}
		if arg == "--quit" {
			cfg.Quit = true
			continue
		}
		if arg == "--restart" {
			cfg.Restart = true
			continue
		}
		if arg == "--gateway" {
			cfg.Gateway = true
			cfg.Listen = ""
			continue
		}
		if arg == "--hostonly" {
			cfg.Hostonly = true
			cfg.Listen = ""
			continue
		}
		if arg == "--verbose" {
			cfg.Log = LogStdout
			cfg.Foreground = true
			continue
		}
		if arg == "--debug" {
			cfg.Log = LogScriptDir
			continue
		}
		if arg == "--uniqlog" {
			cfg.Log = LogUniqLog
			continue
		}
		if arg == "--foreground" {
			cfg.Foreground = true
			continue
		}
		if arg == "--test-auth" {
			cfg.TestAuth = true
			continue
		}
		if arg == "--test" {
			cfg.Test = "1"
			continue
		}
		if arg == "--client-nosspi" || arg == "--client_nosspi" {
			cfg.ClientNoSSPI = true
			continue
		}
		if arg == "-h" || arg == "--help" {
			cfg.Help = true
			continue
		}
		if arg == "--version" {
			cfg.Version = true
			continue
		}
		if arg == "--install" {
			cfg.Install = true
			continue
		}
		if arg == "--uninstall" {
			cfg.Uninstall = true
			continue
		}
		if arg == "--force" {
			cfg.Force = true
			continue
		}
		if arg == "--password" {
			cfg.PasswordAction = true
			continue
		}
		if arg == "--client-password" {
			cfg.ClientPasswordAction = true
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		name, val, ok := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if !ok {
			name, val = strings.TrimPrefix(arg, "--"), "1"
		}
		if err := applyValueFrom(&cfg, strings.ReplaceAll(name, "-", "_"), val, "cli"); err != nil {
			return cfg, fmt.Errorf("command line --%s: %w", name, err)
		}
	}
	if err := loadStoredPasswords(&cfg); err != nil {
		return cfg, err
	}
	normalizeDependencies(&cfg)
	return cfg, nil
}

func preScanConfigPath(args []string) string {
	for _, arg := range args {
		if name, val, ok := strings.Cut(strings.TrimPrefix(arg, "--"), "="); ok && name == keyConfig {
			return val
		}
	}
	return ""
}

func hasBareArg(args []string, name string) bool {
	want := "--" + name
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func applyEnv(cfg *Config) error {
	for _, item := range os.Environ() {
		key, val, ok := strings.Cut(item, "=")
		if !ok || !strings.HasPrefix(key, envPrefix) || len(key) <= len(envPrefix) {
			continue
		}
		name := strings.ToLower(key[len(envPrefix):])
		if isAuxiliaryEnvKey(name) {
			continue
		}
		if err := applyValueFrom(cfg, name, val, "env:"+key); err != nil {
			return fmt.Errorf("environment %s: %w", key, err)
		}
	}
	return nil
}

func applyMap(cfg *Config, values map[string]string, source string) error {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if isAuxiliaryEnvKey(key) {
			continue
		}
		if err := applyValueFrom(cfg, key, values[key], source); err != nil {
			return fmt.Errorf("%s %s: %w", source, key, err)
		}
	}
	return nil
}

func isAuxiliaryEnvKey(name string) bool {
	switch name {
	case keySave, keyDotenv, "keyring_file", "keyring_plaintext", "kerberos_flavor", "kerberos_password", "kerberos_principal", "bin":
		return true
	default:
		return false
	}
}

func loadDotenv() (map[string]string, string, error) {
	values := map[string]string{}
	if explicit, ok := os.LookupEnv(envPrefix + "DOTENV"); ok {
		explicit = strings.TrimSpace(explicit)
		if explicit == "" {
			return values, "", nil
		}
		loaded, err := loadDotenvFile(explicit, values)
		if err != nil {
			return nil, "", fmt.Errorf("load dotenv %s: %w", explicit, err)
		}
		if !loaded {
			return nil, "", fmt.Errorf("load dotenv %s: %w", explicit, os.ErrNotExist)
		}
		return values, "dotenv:" + explicit, nil
	}

	scriptEnv := filepath.Join(GetScriptDir(), ".env")
	loaded, err := loadDotenvFile(scriptEnv, values)
	if err != nil {
		return nil, "", fmt.Errorf("load dotenv %s: %w", scriptEnv, err)
	}
	if loaded {
		return values, "dotenv:" + scriptEnv, nil
	}
	return values, "", nil
}

func loadDotenvFile(path string, values map[string]string) (bool, error) {
	f, err := os.Open(path) // #nosec G703 -- dotenv paths are explicitly selected or resolved from the executable directory.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), maxConfigLineBytes)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return false, fmt.Errorf("line %d: expected KEY=VALUE", lineNo)
		}
		key = strings.TrimSpace(key)
		if !strings.HasPrefix(key, envPrefix) {
			continue
		}
		if _, present := os.LookupEnv(key); present {
			continue
		}
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		values[strings.ToLower(key[len(envPrefix):])] = val
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	return true, nil
}

func canonicalConfigKey(name string) string {
	if name == "proxy" {
		return keyServer
	}
	return name
}

func markSource(cfg *Config, name, source string) {
	if cfg.Sources == nil {
		cfg.Sources = map[string]string{}
	}
	cfg.Sources[canonicalConfigKey(name)] = source
}

func applyValueFrom(cfg *Config, name, val, source string) error {
	if err := applyValue(cfg, name, val); err != nil {
		return err
	}
	markSource(cfg, name, source)
	return nil
}

func applyValue(cfg *Config, name, val string) error {
	switch name {
	case keyServer, "proxy":
		cfg.Server = val
	case keyPAC:
		pac, err := normalizePACLocation(val)
		if err != nil {
			return err
		}
		cfg.PAC = pac
	case keyPACEncoding:
		cfg.PACEncoding = val
	case keyPort:
		parsed, err := parseIntValue(name, val)
		if err != nil {
			return err
		}
		cfg.Port = parsed
	case keyListen:
		cfg.Listen = val
	case keyGateway:
		parsed, err := parseBoolValue(val)
		if err != nil {
			return fmt.Errorf("invalid %s %q: %w", name, val, err)
		}
		cfg.Gateway = parsed
		if cfg.Gateway {
			cfg.Listen = ""
		}
	case keyHostonly:
		parsed, err := parseBoolValue(val)
		if err != nil {
			return fmt.Errorf("invalid %s %q: %w", name, val, err)
		}
		cfg.Hostonly = parsed
		if cfg.Hostonly {
			cfg.Listen = ""
		}
	case keyAllow:
		cfg.Allow = val
	case keyNoProxy:
		cfg.NoProxy = val
	case keyUserAgent:
		cfg.UserAgent = val
	case keyUsername:
		cfg.Username = val
	case keyPassword:
		cfg.Password = val
	case keyClientPassword:
		cfg.ClientPassword = val
	case keyAuth:
		cfg.Auth = strings.ToUpper(val)
	case keyKerberos:
		parsed, err := parseBoolValue(val)
		if err != nil {
			return fmt.Errorf("invalid %s %q: %w", name, val, err)
		}
		cfg.Kerberos = parsed
	case keyWorkers:
		parsed, err := parseIntValue(name, val)
		if err != nil {
			return err
		}
		cfg.Workers = parsed
	case keyThreads:
		parsed, err := parseIntValue(name, val)
		if err != nil {
			return err
		}
		cfg.Threads = parsed
	case keyIdle:
		parsed, err := parseIntValue(name, val)
		if err != nil {
			return err
		}
		cfg.Idle = parsed
	case keySockTimeout:
		parsed, err := parseFloatValue(name, val)
		if err != nil {
			return err
		}
		cfg.SockTimeout = parsed
	case keyProxyReload:
		parsed, err := parseIntValue(name, val)
		if err != nil {
			return err
		}
		cfg.ProxyReload = parsed
	case keyForeground:
		parsed, err := parseBoolValue(val)
		if err != nil {
			return fmt.Errorf("invalid %s %q: %w", name, val, err)
		}
		cfg.Foreground = parsed
	case keyLog:
		parsed, err := parseIntValue(name, val)
		if err != nil {
			return err
		}
		cfg.Log = parsed
	case keyTest:
		cfg.Test = val
	case keyConfig:
		if val == "" {
			cfg.ConfigPath = ""
		} else {
			cfg.ConfigPath = normalizePath(val)
		}
	case keyClientAuth:
		cfg.ClientAuth = strings.ToUpper(val)
	case keyClientUsername:
		cfg.ClientUsername = val
	case keyClientNoSSPI:
		parsed, err := parseBoolValue(val)
		if err != nil {
			return fmt.Errorf("invalid %s %q: %w", name, val, err)
		}
		cfg.ClientNoSSPI = parsed
	default:
		return fmt.Errorf("unsupported option %s", name)
	}
	return nil
}

func parseIntValue(name, val string) (int, error) {
	parsed, err := strconv.Atoi(val)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", name, val, err)
	}
	return parsed, nil
}

func parseFloatValue(name, val string) (float64, error) {
	parsed, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", name, val, err)
	}
	return parsed, nil
}

func parseBoolValue(val string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "", "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	default:
		return false, fmt.Errorf("expected boolean value")
	}
}

func normalizeDependencies(cfg *Config) {
	if cfg.Gateway || cfg.Hostonly {
		cfg.Listen = ""
	}
	if cfg.Hostonly && !cfg.Gateway {
		cfg.Allow = ""
	}
}

func loadStoredPasswords(cfg *Config) error {
	if cfg.Password == "" && cfg.Username != "" {
		pwd, ok, err := getPasswordStrict(Realm, cfg.Username)
		if err != nil {
			return fmt.Errorf("load stored upstream password: %w", err)
		}
		if ok {
			cfg.Password = pwd
		}
	}
	if cfg.ClientPassword == "" && cfg.ClientUsername != "" {
		pwd, ok, err := getPasswordStrict(ClientRealm, cfg.ClientUsername)
		if err != nil {
			return fmt.Errorf("load stored client password: %w", err)
		}
		if ok {
			cfg.ClientPassword = pwd
		}
	}
	return nil
}

func StorePassword(realm, username, password string) error {
	if username == "" {
		return errors.New("username is required")
	}
	if password == "" {
		return errors.New("password is required")
	}
	// PXGO_KEYRING_PLAINTEXT=1 bypasses the OS keyring (useful for Docker/CI).
	if os.Getenv(envPrefix+"KEYRING_PLAINTEXT") == "1" {
		return storePlaintext(realm, username, password)
	}
	if err := keyring.Set(realm, username, password); err == nil {
		return nil
	}
	return errors.New("no keyring backend available; set PXGO_KEYRING_PLAINTEXT=1 for plaintext storage")
}

func GetPassword(realm, username string) (string, bool) {
	pwd, ok, _ := getPasswordStrict(realm, username)
	return pwd, ok
}

func getPasswordStrict(realm, username string) (string, bool, error) {
	if username == "" {
		return "", false, nil
	}
	if os.Getenv(envPrefix+"KEYRING_PLAINTEXT") == "1" {
		return getPlaintextStrict(realm, username)
	}
	if pwd, err := keyring.Get(realm, username); err == nil {
		return pwd, true, nil
	}
	return "", false, nil
}

func storePlaintext(realm, username, password string) error {
	path := keyringPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return withFileLock(path, 0o600, func() error {
		data := map[string]map[string]string{}
		raw, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := json.Unmarshal(raw, &data); err != nil {
				return fmt.Errorf("parse plaintext keyring %s: %w", path, err)
			}
		case errors.Is(err, os.ErrNotExist):
		default:
			return fmt.Errorf("read plaintext keyring %s: %w", path, err)
		}
		if data[realm] == nil {
			data[realm] = map[string]string{}
		}
		data[realm][username] = password
		raw, err = json.MarshalIndent(data, "", "  ")
		if err != nil {
			return err
		}
		return atomicWriteFile(path, raw, 0o600)
	})
}

func getPlaintextStrict(realm, username string) (string, bool, error) {
	path := keyringPath()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read plaintext keyring %s: %w", path, err)
	}
	data := map[string]map[string]string{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", false, fmt.Errorf("parse plaintext keyring %s: %w", path, err)
	}
	password := data[realm][username]
	return password, password != "", nil
}

func keyringPath() string {
	if path := os.Getenv(envPrefix + "KEYRING_FILE"); path != "" {
		return path
	}
	return filepath.Join(GetConfigDir(), "keyring.json")
}

func normalizePACLocation(pac string) (string, error) {
	if pac == "" {
		return "", nil
	}
	if strings.HasPrefix(pac, "http://") || strings.HasPrefix(pac, "https://") {
		u, err := url.Parse(pac)
		if err != nil || u.Host == "" {
			return "", fmt.Errorf("invalid PAC URL %q", pac)
		}
		return pac, nil
	}
	if strings.HasPrefix(pac, "file:") {
		path, err := fileURLToLocalPathStrict(pac)
		if err != nil {
			return "", fmt.Errorf("invalid PAC file URL %q: %w", pac, err)
		}
		if _, err := os.Stat(path); err != nil { // #nosec G703 -- PAC paths are explicitly user-configured.
			return "", fmt.Errorf("PAC path %s: %w", path, err)
		}
		return path, nil
	}
	path := pac
	if !filepath.IsAbs(path) {
		path = filepath.Join(GetScriptDir(), path)
	}
	if _, err := os.Stat(path); err != nil { // #nosec G703 -- PAC paths are explicitly user-configured.
		return "", fmt.Errorf("PAC path %s: %w", path, err)
	}
	return path, nil
}

func ConfigPath(explicit string) string {
	path, _ := configPathStrict(explicit)
	return path
}

func configPathStrict(explicit string) (string, error) {
	if explicit != "" {
		return normalizePath(explicit), nil
	}
	if _, err := os.Stat("pxgo.ini"); err == nil {
		abs, absErr := filepath.Abs("pxgo.ini")
		if absErr != nil {
			return "", absErr
		}
		return abs, nil
	}
	configDir, err := getConfigDirStrict()
	if err != nil {
		return "", err
	}
	configPath := filepath.Join(configDir, "pxgo.ini")
	if _, err := os.Stat(configPath); err == nil {
		return configPath, nil
	}
	scriptPath := filepath.Join(GetScriptDir(), "pxgo.ini")
	if _, err := os.Stat(scriptPath); err == nil {
		return scriptPath, nil
	}
	return configPath, nil
}

func ConfigPathForSave(explicit string) string {
	if explicit != "" {
		return normalizePath(explicit)
	}
	cwdPath, _ := filepath.Abs("pxgo.ini")
	if isWritableFile(cwdPath) {
		return cwdPath
	}
	configPath := filepath.Join(GetConfigDir(), "pxgo.ini")
	if _, err := os.Stat(configPath); err == nil {
		return configPath
	}
	scriptPath := filepath.Join(GetScriptDir(), "pxgo.ini")
	if isWritableFile(scriptPath) {
		return scriptPath
	}
	return configPath
}

func normalizePath(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return abs
}

func GetScriptDir() string {
	exe, err := executablePath()
	if err != nil || exe == "" {
		return "."
	}
	return filepath.Dir(exe)
}

func GetScriptCmd() string {
	if len(os.Args) > 0 && os.Args[0] != "" {
		return os.Args[0]
	}
	exe, err := executablePath()
	if err != nil || exe == "" {
		return "pxgo"
	}
	return exe
}

func IsCompiled() bool {
	return true
}

func isWritableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func SaveINI(path string, cfg Config) error {
	if path == "" {
		return errors.New("empty config path")
	}
	if err := validateINIStrings(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	listen := cfg.Listen
	if cfg.Gateway || cfg.Hostonly {
		listen = ""
	}
	content := fmt.Sprintf(`[proxy]
server = %s
pac = %s
pac_encoding = %s
port = %d
listen = %s
gateway = %d
hostonly = %d
allow = %s
noproxy = %s
useragent = %s
username = %s
auth = %s
kerberos = %d

[client]
client_auth = %s
client_username = %s
client_nosspi = %d

[settings]
workers = %d
threads = %d
idle = %d
socktimeout = %g
proxyreload = %d
foreground = %d
log = %d
`, cfg.Server, cfg.PAC, cfg.PACEncoding, cfg.Port, listen, btoi(cfg.Gateway), btoi(cfg.Hostonly), cfg.Allow, cfg.NoProxy,
		cfg.UserAgent, cfg.Username, cfg.Auth, btoi(cfg.Kerberos), cfg.ClientAuth, cfg.ClientUsername, btoi(cfg.ClientNoSSPI), cfg.Workers, cfg.Threads, cfg.Idle,
		cfg.SockTimeout, cfg.ProxyReload, btoi(cfg.Foreground), cfg.Log)
	return withFileLock(path, 0o600, func() error {
		return atomicWriteFile(path, []byte(content), 0o600)
	})
}

func validateINIStrings(cfg Config) error {
	fields := []struct {
		name  string
		value string
	}{
		{"server", cfg.Server},
		{"pac", cfg.PAC},
		{"pac_encoding", cfg.PACEncoding},
		{"listen", cfg.Listen},
		{"allow", cfg.Allow},
		{"noproxy", cfg.NoProxy},
		{"useragent", cfg.UserAgent},
		{"username", cfg.Username},
		{"auth", cfg.Auth},
		{"client_auth", cfg.ClientAuth},
		{"client_username", cfg.ClientUsername},
	}
	for _, field := range fields {
		if strings.ContainsAny(field.value, "\r\n") {
			return fmt.Errorf("%s contains a line break", field.name)
		}
	}
	return nil
}

func btoi(v bool) int {
	if v {
		return 1
	}
	return 0
}

func ReadINI(path string) (Config, error) {
	cfg := Default()
	// #nosec G703 -- config paths are explicitly user-controlled inputs.
	f, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), maxConfigLineBytes)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "[") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return cfg, fmt.Errorf("%s:%d: expected key=value", path, lineNo)
		}
		if err := applyValueFrom(&cfg, strings.TrimSpace(k), strings.TrimSpace(v), "ini:"+path); err != nil {
			return cfg, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	normalizeDependencies(&cfg)
	return cfg, nil
}
