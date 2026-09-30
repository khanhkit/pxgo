package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	checksumsAssetName = "checksums.txt"
	maxChecksumsBytes  = 2 << 20
	maxArchiveBytes    = 512 << 20
	maxCandidateBytes  = 256 << 20
)

var ErrNoUpdate = errors.New("no update available")

type URLValidator func(raw string, redirect bool) error

type Stager struct {
	Checker          Checker
	Client           *http.Client
	GOOS             string
	GOARCH           string
	TempDir          string
	ValidateURL      URLValidator
	VerifyCandidate  func(context.Context, string, string) error
	CandidateTimeout time.Duration
}

type StagedCandidate struct {
	Check       CheckResult
	AssetName   string
	ArchivePath string
	BinaryPath  string
	SHA256      string
	Dir         string
}

func (s StagedCandidate) Cleanup() error {
	if strings.TrimSpace(s.Dir) == "" {
		return nil
	}
	return os.RemoveAll(s.Dir)
}

func (s Stager) Stage(ctx context.Context, current string) (StagedCandidate, error) {
	if ctx == nil {
		return StagedCandidate{}, errors.New("nil context")
	}
	checker := s.Checker
	if checker.Client == nil && s.Client != nil {
		checker.Client = s.Client
	}
	check, err := checker.Check(ctx, current)
	if err != nil {
		return StagedCandidate{}, err
	}
	return s.StageCheck(ctx, check)
}

func (s Stager) StageCheck(ctx context.Context, check CheckResult) (StagedCandidate, error) {
	if ctx == nil {
		return StagedCandidate{}, errors.New("nil context")
	}
	if !check.Available {
		return StagedCandidate{}, ErrNoUpdate
	}
	goos := s.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := s.GOARCH
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	assetName := AssetName(goos, goarch, check.Latest)
	asset, err := exactAsset(check.Release.Assets, assetName)
	if err != nil {
		return StagedCandidate{}, err
	}
	checksumsAsset, err := exactAsset(check.Release.Assets, checksumsAssetName)
	if err != nil {
		return StagedCandidate{}, err
	}

	validator := s.ValidateURL
	if validator == nil {
		validator = ValidateDownloadURL
	}
	client := s.Client
	if client == nil {
		client = s.Checker.Client
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	client = clientWithRedirectValidation(client, validator)

	checksums, err := downloadBytes(ctx, client, checksumsAsset.URL, maxChecksumsBytes, validator)
	if err != nil {
		return StagedCandidate{}, fmt.Errorf("download checksums: %w", err)
	}
	checksum, err := ChecksumForAsset(bytes.NewReader(checksums), assetName)
	if err != nil {
		return StagedCandidate{}, err
	}
	if asset.Digest != "" {
		digest := strings.TrimSpace(asset.Digest)
		if !strings.HasPrefix(strings.ToLower(digest), "sha256:") {
			return StagedCandidate{}, errors.New("release asset digest is not sha256")
		}
		if !strings.EqualFold(strings.TrimPrefix(strings.ToLower(digest), "sha256:"), checksum) {
			return StagedCandidate{}, errors.New("release asset digest disagrees with checksums.txt")
		}
	}

	dir, err := os.MkdirTemp(s.TempDir, "pxgo-update-")
	if err != nil {
		return StagedCandidate{}, err
	}
	staged := StagedCandidate{Check: check, AssetName: assetName, Dir: dir}
	cleanup := true
	defer func() {
		if cleanup {
			_ = staged.Cleanup()
		}
	}()

	archivePath := filepath.Join(dir, assetName)
	archiveSHA, err := downloadFile(ctx, client, asset.URL, archivePath, maxArchiveBytes, validator)
	if err != nil {
		return StagedCandidate{}, fmt.Errorf("download candidate: %w", err)
	}
	if !strings.EqualFold(archiveSHA, checksum) {
		return StagedCandidate{}, errors.New("candidate sha256 does not match checksums.txt")
	}
	if asset.Digest != "" && !strings.EqualFold("sha256:"+archiveSHA, strings.TrimSpace(asset.Digest)) {
		return StagedCandidate{}, errors.New("candidate sha256 does not match release asset digest")
	}

	binaryName := productName
	if goos == goosWindows {
		binaryName = "pxgo.exe"
	}
	binaryPath := filepath.Join(dir, binaryName)
	if err := extractCandidate(archivePath, assetName, binaryName, binaryPath); err != nil {
		return StagedCandidate{}, err
	}
	if goos == runtime.GOOS && goarch == runtime.GOARCH {
		verify := s.VerifyCandidate
		if verify == nil {
			verify = verifyCandidateVersion
		}
		timeout := s.CandidateTimeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		verifyCtx, cancel := context.WithTimeout(ctx, timeout)
		err = verify(verifyCtx, binaryPath, check.Latest)
		cancel()
		if err != nil {
			return StagedCandidate{}, err
		}
	}

	staged.ArchivePath = archivePath
	staged.BinaryPath = binaryPath
	staged.SHA256 = archiveSHA
	cleanup = false
	return staged, nil
}

func exactAsset(assets []Asset, name string) (Asset, error) {
	var match Asset
	count := 0
	for _, asset := range assets {
		if asset.Name == name {
			match = asset
			count++
		}
	}
	switch count {
	case 0:
		return Asset{}, fmt.Errorf("release asset %q not found", name)
	case 1:
		return match, nil
	default:
		return Asset{}, fmt.Errorf("release asset %q is ambiguous", name)
	}
}

func ValidateDownloadURL(raw string, redirect bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" || u.User != nil {
		return errors.New("release download URL must use https without userinfo")
	}
	host := strings.ToLower(u.Hostname())
	if host == "github.com" {
		if !strings.HasPrefix(u.EscapedPath(), "/khanhkit/pxgo/releases/download/") {
			return errors.New("release download URL is outside pinned repository")
		}
		return nil
	}
	if redirect && (host == "release-assets.githubusercontent.com" || host == "objects.githubusercontent.com") {
		return nil
	}
	return fmt.Errorf("release download host %q is not allowed", host)
}

func clientWithRedirectValidation(base *http.Client, validator URLValidator) *http.Client {
	clone := *base
	prior := base.CheckRedirect
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := validator(req.URL.String(), true); err != nil {
			return err
		}
		if prior != nil {
			return prior(req, via)
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	return &clone
}

func downloadBytes(ctx context.Context, client *http.Client, rawURL string, limit int64, validator URLValidator) ([]byte, error) {
	if err := validator(rawURL, false); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("download exceeds size limit")
	}
	return data, nil
}

func downloadFile(ctx context.Context, client *http.Client, rawURL, path string, limit int64, validator URLValidator) (string, error) {
	if err := validator(rawURL, false); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, h), io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", err
	}
	if written > limit {
		return "", errors.New("download exceeds size limit")
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func extractCandidate(archivePath, assetName, binaryName, destination string) error {
	switch {
	case strings.HasSuffix(assetName, ".zip"):
		return extractZipCandidate(archivePath, binaryName, destination)
	case strings.HasSuffix(assetName, ".tar.gz"):
		return extractTarCandidate(archivePath, binaryName, destination)
	default:
		return fmt.Errorf("unsupported release archive %q", assetName)
	}
}

func extractZipCandidate(archivePath, binaryName, destination string) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	var match *zip.File
	for _, entry := range archive.File {
		if filepath.ToSlash(entry.Name) != binaryName {
			continue
		}
		if match != nil {
			return errors.New("release archive contains duplicate candidate binary")
		}
		match = entry
	}
	if match == nil {
		return errors.New("release archive does not contain candidate binary")
	}
	if match.FileInfo().IsDir() || match.UncompressedSize64 > maxCandidateBytes {
		return errors.New("candidate binary entry is invalid")
	}
	r, err := match.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	return writeCandidate(destination, r, int64(match.UncompressedSize64))
}

func extractTarCandidate(archivePath, binaryName, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	found := false
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		if filepath.ToSlash(header.Name) != binaryName {
			continue
		}
		if found {
			return errors.New("release archive contains duplicate candidate binary")
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > maxCandidateBytes {
			return errors.New("candidate binary entry is invalid")
		}
		if err := writeCandidate(destination, reader, header.Size); err != nil {
			return err
		}
		found = true
	}
	if !found {
		return errors.New("release archive does not contain candidate binary")
	}
	return nil
}

func writeCandidate(destination string, source io.Reader, expected int64) error {
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, io.LimitReader(source, maxCandidateBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxCandidateBytes || written != expected {
		return errors.New("candidate binary size mismatch")
	}
	return nil
}

func verifyCandidateVersion(ctx context.Context, path, want string) error {
	// #nosec G204 -- path is the verified candidate extracted from the exact staged release archive.
	cmd := exec.CommandContext(ctx, path, "--version")
	configureHiddenProcess(cmd)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("candidate version check: %w", err)
	}
	got, err := normalizeVersion(strings.TrimSpace(string(output)))
	if err != nil {
		return fmt.Errorf("candidate version output: %w", err)
	}
	wantVersion, err := normalizeVersion(want)
	if err != nil {
		return err
	}
	if got != wantVersion {
		return fmt.Errorf("candidate version %s does not match release %s", got, wantVersion)
	}
	return nil
}
