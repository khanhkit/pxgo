package kerberos

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jcmturner/gokrb5/v8/client"
	krbconfig "github.com/jcmturner/gokrb5/v8/config"
	"github.com/jcmturner/gokrb5/v8/credentials"
	"github.com/jcmturner/gokrb5/v8/spnego"
)

// SPNEGOToken returns an RFC 4178 Negotiate token for HTTP/<proxyHost> using
// the FILE credential cache owned by this Manager. A fresh gokrb5 client is
// created for each exchange so ticket refreshes committed by Manager are seen
// without retaining password material or stale ticket generations.
func (m *Manager) SPNEGOToken(proxyHost string) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("kerberos manager is nil")
	}
	host := normalizeProxyHost(proxyHost)
	if host == "" {
		return nil, fmt.Errorf("kerberos proxy host is empty")
	}

	ccachePath, err := fileCCachePath(m.CCacheName)
	if err != nil {
		return nil, err
	}
	if err := waitForInitialCCache(m, ccachePath, 5*time.Second); err != nil {
		return nil, err
	}
	ccache, err := credentials.LoadCCache(ccachePath)
	if err != nil {
		return nil, fmt.Errorf("load Kerberos credential cache %s: %w", ccachePath, err)
	}

	configPath, err := resolveKRB5Config(m.Env)
	if err != nil {
		return nil, err
	}
	krb5conf, err := krbconfig.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load Kerberos config %s: %w", configPath, err)
	}

	cli, err := client.NewFromCCache(ccache, krb5conf, client.DisablePAFXFAST(true))
	if err != nil {
		return nil, fmt.Errorf("create Kerberos client from credential cache: %w", err)
	}
	defer cli.Destroy()

	negotiator := spnego.SPNEGOClient(cli, "HTTP/"+host)
	if err := negotiator.AcquireCred(); err != nil {
		return nil, fmt.Errorf("acquire Kerberos SPNEGO credential: %w", err)
	}
	token, err := negotiator.InitSecContext()
	if err != nil {
		return nil, fmt.Errorf("initialize Kerberos SPNEGO context for HTTP/%s: %w", host, err)
	}
	raw, err := token.Marshal()
	if err != nil {
		return nil, fmt.Errorf("marshal Kerberos SPNEGO token: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("kerberos SPNEGO produced an empty token")
	}
	return raw, nil
}

func waitForInitialCCache(m *Manager, path string, timeout time.Duration) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if m == nil || !m.Status().Refreshing {
		return fmt.Errorf("kerberos credential cache %s is not ready", path)
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if !m.Status().Refreshing {
			return fmt.Errorf("kerberos credential cache %s was not created by refresh", path)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for kerberos credential cache %s", path)
		}
		<-ticker.C
	}
}

func fileCCachePath(name string) (string, error) {
	const prefix = "FILE:"
	if !strings.HasPrefix(name, prefix) {
		return "", fmt.Errorf("unsupported Kerberos credential cache %q: pxgo requires a FILE cache", name)
	}
	path := strings.TrimSpace(strings.TrimPrefix(name, prefix))
	if path == "" {
		return "", fmt.Errorf("kerberos credential cache path is empty")
	}
	return path, nil
}

func resolveKRB5Config(env map[string]string) (string, error) {
	if raw := strings.TrimSpace(env["KRB5_CONFIG"]); raw != "" {
		for _, path := range filepath.SplitList(raw) {
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path, nil
			}
		}
		return "", fmt.Errorf("KRB5_CONFIG does not contain a readable config file: %s", raw)
	}
	for _, path := range []string{
		"/etc/krb5.conf",
		"/Library/Preferences/edu.mit.Kerberos",
	} {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("kerberos config not found; set KRB5_CONFIG")
}

func normalizeProxyHost(proxyHost string) string {
	host := strings.TrimSpace(proxyHost)
	if strings.HasPrefix(host, "[") {
		if end := strings.Index(host, "]"); end > 0 {
			return host[1:end]
		}
	}
	if i := strings.LastIndex(host, ":"); i > 0 && strings.Count(host, ":") == 1 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}
