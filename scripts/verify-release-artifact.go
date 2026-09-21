//go:build ignore

package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func main() {
	var dist, targetOS, targetArch, expectedVersion string
	flag.StringVar(&dist, "dist", "dist", "candidate dist directory")
	flag.StringVar(&targetOS, "os", runtime.GOOS, "target OS")
	flag.StringVar(&targetArch, "arch", runtime.GOARCH, "target architecture")
	flag.StringVar(&expectedVersion, "version", "", "expected release version without leading v")
	flag.Parse()

	if err := verify(dist, targetOS, targetArch, expectedVersion); err != nil {
		fmt.Fprintln(os.Stderr, "release-artifact verification failed:", err)
		os.Exit(1)
	}
	fmt.Printf("release-artifact verification passed for %s/%s\n", targetOS, targetArch)
}

func verify(dist, targetOS, targetArch, expectedVersion string) error {
	ext := ".tar.gz"
	binaryName := "pxgo"
	if targetOS == "windows" {
		ext = ".zip"
		binaryName = "pxgo.exe"
	}
	archiveName := fmt.Sprintf("pxgo_%s_%s%s", targetOS, targetArch, ext)
	archivePath := filepath.Join(dist, archiveName)

	if err := verifyChecksum(filepath.Join(dist, "checksums.txt"), archiveName, archivePath); err != nil {
		return err
	}

	extractDir, err := os.MkdirTemp("", "pxgo-release-artifact-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(extractDir)

	if targetOS == "windows" {
		err = extractZip(archivePath, extractDir)
	} else {
		err = extractTarGz(archivePath, extractDir)
	}
	if err != nil {
		return err
	}

	binaryPath, err := findFile(extractDir, binaryName)
	if err != nil {
		return err
	}
	if targetOS != "windows" {
		if err := os.Chmod(binaryPath, 0o755); err != nil {
			return fmt.Errorf("chmod candidate binary: %w", err)
		}
	}

	versionOut, err := exec.Command(binaryPath, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("--version: %w: %s", err, versionOut)
	}
	gotVersion := strings.TrimSpace(string(versionOut))
	if gotVersion == "" || gotVersion == "dev" {
		return fmt.Errorf("invalid release version %q", gotVersion)
	}
	if expectedVersion != "" && gotVersion != expectedVersion {
		return fmt.Errorf("version=%q want %q", gotVersion, expectedVersion)
	}

	helpOut, err := exec.Command(binaryPath, "--help").CombinedOutput()
	if err != nil {
		return fmt.Errorf("--help: %w: %s", err, helpOut)
	}
	if !strings.Contains(string(helpOut), "Usage:") {
		return errors.New("--help output missing Usage")
	}

	if err := smokeProxy(binaryPath); err != nil {
		return err
	}
	return nil
}

func verifyChecksum(checksumPath, archiveName, archivePath string) error {
	data, err := os.ReadFile(checksumPath)
	if err != nil {
		return fmt.Errorf("read checksums: %w", err)
	}
	want := ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimPrefix(fields[len(fields)-1], "*") == archiveName {
			want = fields[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("checksum entry missing for %s", archiveName)
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("archive checksum=%s want=%s", got, want)
	}
	return nil
}

func extractTarGz(path, dest string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
}

func extractZip(path, dest string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		target, err := safeJoin(dest, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		in.Close()
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func safeJoin(root, name string) (string, error) {
	clean := filepath.Clean(name)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return filepath.Join(root, clean), nil
}

func findFile(root, name string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(d.Name(), name) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("%s not found in archive", name)
	}
	return found, nil
}

func smokeProxy(binaryPath string) error {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "artifact-ok")
	}))
	defer upstream.Close()

	port, err := freePort()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath,
		"--port="+strconv.Itoa(port),
		"--listen=127.0.0.1",
		"--proxy=DIRECT",
	)
	cmd.Env = append(os.Environ(), "PXGO_PROXY=DIRECT")
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start packaged proxy: %w", err)
	}

	if err := waitForPort(port, 8*time.Second); err != nil {
		cancel()
		_ = cmd.Wait()
		return fmt.Errorf("%w\n%s", err, output.String())
	}

	proxyURL := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 5 * time.Second}
	resp, err := client.Get(upstream.URL)
	if err != nil {
		cancel()
		_ = cmd.Wait()
		return fmt.Errorf("proxy smoke request: %w\n%s", err, output.String())
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil || resp.StatusCode != http.StatusOK || string(body) != "artifact-ok" {
		cancel()
		_ = cmd.Wait()
		return fmt.Errorf("proxy smoke response status=%s body=%q err=%v", resp.Status, body, readErr)
	}

	quitOut, err := exec.Command(binaryPath, "--port="+strconv.Itoa(port), "--quit").CombinedOutput()
	if err != nil {
		cancel()
		_ = cmd.Wait()
		return fmt.Errorf("packaged proxy quit: %w: %s", err, quitOut)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("packaged proxy exit: %w\n%s", err, output.String())
		}
	case <-time.After(8 * time.Second):
		cancel()
		return errors.New("packaged proxy did not stop after --quit")
	}
	return nil
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func waitForPort(port int, timeout time.Duration) error {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 150*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("%s did not open", addr)
}
