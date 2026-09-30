package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const DefaultRepository = "khanhkit/pxgo"
const defaultAPIBase = "https://api.github.com"
const productName = "pxgo"
const goosWindows = "windows"

type Channel string

const (
	Stable     Channel = "stable"
	Prerelease Channel = "prerelease"
)

type Asset struct{ Name, URL, Digest string }
type Release struct {
	Version    string
	Tag        string
	Prerelease bool
	Assets     []Asset
}
type CheckResult struct {
	Current   string
	Latest    string
	Available bool
	Release   Release
}
type Checker struct {
	Client     *http.Client
	APIBase    string
	Repository string
	Channel    Channel
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Digest             string `json:"digest"`
	} `json:"assets"`
}

func (c Checker) Check(ctx context.Context, current string) (CheckResult, error) {
	if ctx == nil {
		return CheckResult{}, errors.New("nil context")
	}
	base := strings.TrimRight(c.APIBase, "/")
	if base == "" {
		base = defaultAPIBase
	}
	repo := c.Repository
	if repo == "" {
		repo = DefaultRepository
	}
	if !validRepository(repo) {
		return CheckResult{}, errors.New("invalid release repository")
	}
	channel := c.Channel
	if channel == "" {
		channel = Stable
	}
	if channel != Stable && channel != Prerelease {
		return CheckResult{}, fmt.Errorf("unsupported update channel %q", channel)
	}
	endpoint := base + "/repos/" + repo + "/releases"
	if channel == Stable {
		endpoint += "/latest"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return CheckResult{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	var rel githubRelease
	if channel == Stable {
		if err := getJSON(client, req, &rel); err != nil {
			return CheckResult{}, err
		}
		if rel.Draft || rel.Prerelease {
			return CheckResult{}, errors.New("latest stable endpoint returned non-stable release")
		}
	} else {
		var releases []githubRelease
		if err := getJSON(client, req, &releases); err != nil {
			return CheckResult{}, err
		}
		var bestVersion string
		found := false
		for _, candidate := range releases {
			if candidate.Draft {
				continue
			}
			candidateVersion, normalizeErr := normalizeVersion(candidate.TagName)
			if normalizeErr != nil {
				continue
			}
			if !found || CompareVersions(candidateVersion, bestVersion) > 0 {
				rel = candidate
				bestVersion = candidateVersion
				found = true
			}
		}
		if !found {
			return CheckResult{}, errors.New("no accepted release found")
		}
	}
	latest, err := normalizeVersion(rel.TagName)
	if err != nil {
		return CheckResult{}, fmt.Errorf("invalid release tag: %w", err)
	}
	currentVersion, err := normalizeVersion(current)
	if err != nil {
		return CheckResult{}, fmt.Errorf("invalid current version: %w", err)
	}
	release := Release{Version: latest, Tag: rel.TagName, Prerelease: rel.Prerelease}
	for _, asset := range rel.Assets {
		release.Assets = append(release.Assets, Asset{Name: asset.Name, URL: asset.BrowserDownloadURL, Digest: asset.Digest})
	}
	return CheckResult{Current: currentVersion, Latest: latest, Available: CompareVersions(latest, currentVersion) > 0, Release: release}, nil
}

func getJSON(client *http.Client, req *http.Request, dst any) error {
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("release discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("release discovery: HTTP %d", resp.StatusCode)
	}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("release discovery response: %w", err)
	}
	return nil
}

func validRepository(repo string) bool {
	parts := strings.Split(repo, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.ContainsAny(repo, "?#\\")
}

type semanticVersion struct {
	core       [3]uint64
	prerelease []string
}

func normalizeVersion(value string) (string, error) {
	v := strings.TrimPrefix(strings.TrimSpace(value), "v")
	if v == "" {
		return "", fmt.Errorf("expected semantic version, got %q", value)
	}
	withoutBuild := strings.SplitN(v, "+", 2)[0]
	core, prerelease, _ := strings.Cut(withoutBuild, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("expected semantic version, got %q", value)
	}
	for _, p := range parts {
		if p == "" {
			return "", fmt.Errorf("expected semantic version, got %q", value)
		}
		if len(p) > 1 && p[0] == '0' {
			return "", fmt.Errorf("expected semantic version, got %q", value)
		}
		if _, err := strconv.ParseUint(p, 10, 64); err != nil {
			return "", fmt.Errorf("expected semantic version, got %q", value)
		}
	}
	if strings.Contains(withoutBuild, "-") {
		if prerelease == "" {
			return "", fmt.Errorf("expected semantic version, got %q", value)
		}
		for _, identifier := range strings.Split(prerelease, ".") {
			if identifier == "" || !validSemverIdentifier(identifier) {
				return "", fmt.Errorf("expected semantic version, got %q", value)
			}
			if isNumeric(identifier) && len(identifier) > 1 && identifier[0] == '0' {
				return "", fmt.Errorf("expected semantic version, got %q", value)
			}
		}
	}
	return withoutBuild, nil
}

func validSemverIdentifier(value string) bool {
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	return true
}

func isNumeric(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseSemanticVersion(value string) (semanticVersion, error) {
	normalized, err := normalizeVersion(value)
	if err != nil {
		return semanticVersion{}, err
	}
	core, prerelease, hasPrerelease := strings.Cut(normalized, "-")
	parts := strings.Split(core, ".")
	var parsed semanticVersion
	for i := range parsed.core {
		parsed.core[i], _ = strconv.ParseUint(parts[i], 10, 64)
	}
	if hasPrerelease {
		parsed.prerelease = strings.Split(prerelease, ".")
	}
	return parsed, nil
}

func CompareVersions(a, b string) int {
	av, aErr := parseSemanticVersion(a)
	bv, bErr := parseSemanticVersion(b)
	if aErr != nil || bErr != nil {
		return 0
	}
	for i := 0; i < 3; i++ {
		if av.core[i] < bv.core[i] {
			return -1
		}
		if av.core[i] > bv.core[i] {
			return 1
		}
	}
	if len(av.prerelease) == 0 && len(bv.prerelease) == 0 {
		return 0
	}
	if len(av.prerelease) == 0 {
		return 1
	}
	if len(bv.prerelease) == 0 {
		return -1
	}
	limit := len(av.prerelease)
	if len(bv.prerelease) < limit {
		limit = len(bv.prerelease)
	}
	for i := 0; i < limit; i++ {
		if av.prerelease[i] == bv.prerelease[i] {
			continue
		}
		aNumeric := isNumeric(av.prerelease[i])
		bNumeric := isNumeric(bv.prerelease[i])
		switch {
		case aNumeric && bNumeric:
			aValue, _ := strconv.ParseUint(av.prerelease[i], 10, 64)
			bValue, _ := strconv.ParseUint(bv.prerelease[i], 10, 64)
			if aValue < bValue {
				return -1
			}
			return 1
		case aNumeric:
			return -1
		case bNumeric:
			return 1
		default:
			if av.prerelease[i] < bv.prerelease[i] {
				return -1
			}
			return 1
		}
	}
	if len(av.prerelease) < len(bv.prerelease) {
		return -1
	}
	if len(av.prerelease) > len(bv.prerelease) {
		return 1
	}
	return 0
}

type Provider string

const (
	ProviderDirect Provider = "direct"
	ProviderWinGet Provider = "winget"
	ProviderScoop  Provider = "scoop"
	ProviderBrew   Provider = "homebrew"
)

type CommandRunner interface {
	Run(context.Context, string, ...string) error
}
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) error {
	// #nosec G204 -- provider command and argv are fixed below; no shell is used.
	return exec.CommandContext(ctx, name, args...).Run()
}

func Upgrade(ctx context.Context, provider Provider, runner CommandRunner) error {
	if runner == nil {
		return errors.New("nil update command runner")
	}
	var name string
	var args []string
	switch provider {
	case ProviderWinGet:
		name, args = "winget", []string{"upgrade", "--id", "KhanhKit.PxGo", "--exact", "--disable-interactivity", "--accept-source-agreements", "--accept-package-agreements"}
	case ProviderScoop:
		name, args = "scoop", []string{"update", productName}
	case ProviderBrew:
		name, args = "brew", []string{"upgrade", productName}
	case ProviderDirect:
		return errors.New("direct updates require verified staged replacement")
	default:
		return fmt.Errorf("unsupported update provider %q", provider)
	}
	if err := runner.Run(ctx, name, args...); err != nil {
		return fmt.Errorf("%s update failed: %w", provider, err)
	}
	return nil
}

func AssetName(goos, goarch, _ string) string {
	ext := "tar.gz"
	if goos == goosWindows {
		ext = "zip"
	}
	return fmt.Sprintf("pxgo_%s_%s.%s", goos, goarch, ext)
}
func CurrentAssetName(version string) string { return AssetName(runtime.GOOS, runtime.GOARCH, version) }

func VerifySHA256(r io.Reader, digest string) error {
	if r == nil {
		return errors.New("nil candidate reader")
	}
	const prefix = "sha256:"
	if !strings.HasPrefix(strings.ToLower(digest), prefix) {
		return errors.New("candidate digest must be sha256")
	}
	want := strings.TrimSpace(digest[len(prefix):])
	if len(want) != sha256.Size*2 {
		return errors.New("candidate sha256 digest has invalid length")
	}
	if _, err := hex.DecodeString(want); err != nil {
		return errors.New("candidate sha256 digest is malformed")
	}
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return fmt.Errorf("hash candidate: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return errors.New("candidate sha256 digest mismatch")
	}
	return nil
}

func ChecksumForAsset(checksums io.Reader, assetName string) (string, error) {
	if checksums == nil {
		return "", errors.New("nil checksums reader")
	}
	if strings.TrimSpace(assetName) == "" || strings.ContainsAny(assetName, "/\\") {
		return "", errors.New("invalid asset name")
	}
	scanner := bufio.NewScanner(checksums)
	var match string
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name != assetName {
			continue
		}
		if match != "" {
			return "", errors.New("duplicate checksum entry for asset")
		}
		if len(fields[0]) != sha256.Size*2 {
			return "", errors.New("asset checksum has invalid length")
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return "", errors.New("asset checksum is malformed")
		}
		match = strings.ToLower(fields[0])
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read checksums: %w", err)
	}
	if match == "" {
		return "", errors.New("asset checksum not found")
	}
	return match, nil
}

func ValidateAssetURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "github.com") {
		return errors.New("release asset URL must use https://github.com")
	}
	if !strings.HasPrefix(u.EscapedPath(), "/khanhkit/pxgo/releases/download/") {
		return errors.New("release asset URL is outside pinned repository")
	}
	return nil
}
