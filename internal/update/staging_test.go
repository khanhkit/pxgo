package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStagerVerifiesExactReleaseCandidate(t *testing.T) {
	payload := []byte("verified candidate bytes")
	assetName := AssetName(runtime.GOOS, runtime.GOARCH, "1.1.0")
	binaryName := "pxgo"
	if runtime.GOOS == "windows" {
		binaryName = "pxgo.exe"
	}
	archive := makeCandidateArchive(t, assetName, binaryName, payload)
	sum := sha256.Sum256(archive)
	hexSum := hex.EncodeToString(sum[:])

	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/khanhkit/pxgo/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v1.1.0", "draft": false, "prerelease": false,
				"assets": []map[string]string{
					{"name": assetName, "browser_download_url": base + "/asset", "digest": "sha256:" + hexSum},
					{"name": checksumsAssetName, "browser_download_url": base + "/checksums", "digest": ""},
				},
			})
		case "/checksums":
			_, _ = fmt.Fprintf(w, "%s  %s\n", hexSum, assetName)
		case "/asset":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	base = srv.URL

	verified := false
	stager := Stager{
		Checker: Checker{APIBase: srv.URL, Client: srv.Client()},
		Client:  srv.Client(),
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
		TempDir: t.TempDir(),
		ValidateURL: func(raw string, _ bool) error {
			u, err := url.Parse(raw)
			if err != nil {
				return err
			}
			want, _ := url.Parse(srv.URL)
			if u.Scheme != want.Scheme || u.Host != want.Host {
				return fmt.Errorf("unexpected test origin %s", u.Host)
			}
			return nil
		},
		VerifyCandidate: func(_ context.Context, path, want string) error {
			got, err := osReadFile(path)
			if err != nil {
				return err
			}
			if !bytes.Equal(got, payload) || want != "1.1.0" {
				return fmt.Errorf("candidate mismatch")
			}
			verified = true
			return nil
		},
	}
	staged, err := stager.Stage(context.Background(), "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Cleanup()
	if !verified || staged.AssetName != assetName || staged.SHA256 != hexSum || staged.Check.Latest != "1.1.0" {
		t.Fatalf("unexpected staged candidate: %+v verified=%v", staged, verified)
	}
}

func TestStagerRejectsDigestChecksumDisagreement(t *testing.T) {
	assetName := AssetName(runtime.GOOS, runtime.GOARCH, "1.1.0")
	bad := strings.Repeat("a", 64)
	good := strings.Repeat("b", 64)
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/khanhkit/pxgo/releases/latest":
			_, _ = fmt.Fprintf(w, `{"tag_name":"v1.1.0","draft":false,"prerelease":false,"assets":[{"name":%q,"browser_download_url":%q,"digest":%q},{"name":"checksums.txt","browser_download_url":%q}]}`,
				assetName, base+"/asset", "sha256:"+bad, base+"/checksums")
		case "/checksums":
			_, _ = fmt.Fprintf(w, "%s  %s\n", good, assetName)
		default:
			_, _ = w.Write([]byte("unused"))
		}
	}))
	defer srv.Close()
	base = srv.URL
	validator := testOriginValidator(t, srv.URL)
	_, err := (Stager{Checker: Checker{APIBase: srv.URL, Client: srv.Client()}, Client: srv.Client(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, TempDir: t.TempDir(), ValidateURL: validator}).Stage(context.Background(), "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "disagrees") {
		t.Fatalf("expected digest/checksum disagreement, got %v", err)
	}
}

func TestStagerRejectsMissingExactAsset(t *testing.T) {
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"tag_name":"v1.1.0","draft":false,"prerelease":false,"assets":[{"name":"pxgo_wrong.zip","browser_download_url":%q,"digest":"sha256:%s"},{"name":"checksums.txt","browser_download_url":%q}]}`,
			base+"/asset", strings.Repeat("a", 64), base+"/checksums")
	}))
	defer srv.Close()
	base = srv.URL
	_, err := (Stager{Checker: Checker{APIBase: srv.URL, Client: srv.Client()}, Client: srv.Client(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, TempDir: t.TempDir(), ValidateURL: testOriginValidator(t, srv.URL)}).Stage(context.Background(), "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected missing asset error, got %v", err)
	}
}

func TestStagerRejectsRedirectOutsideAllowedOrigin(t *testing.T) {
	assetName := AssetName(runtime.GOOS, runtime.GOARCH, "1.1.0")
	archive := []byte("not reached")
	sum := sha256.Sum256(archive)
	hexSum := hex.EncodeToString(sum[:])
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/khanhkit/pxgo/releases/latest":
			_, _ = fmt.Fprintf(w, `{"tag_name":"v1.1.0","draft":false,"prerelease":false,"assets":[{"name":%q,"browser_download_url":%q,"digest":%q},{"name":"checksums.txt","browser_download_url":%q}]}`,
				assetName, base+"/redirect", "sha256:"+hexSum, base+"/checksums")
		case "/checksums":
			_, _ = fmt.Fprintf(w, "%s  %s\n", hexSum, assetName)
		case "/redirect":
			http.Redirect(w, r, "https://example.com/evil", http.StatusFound)
		}
	}))
	defer srv.Close()
	base = srv.URL
	_, err := (Stager{Checker: Checker{APIBase: srv.URL, Client: srv.Client()}, Client: srv.Client(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, TempDir: t.TempDir(), ValidateURL: testOriginValidator(t, srv.URL)}).Stage(context.Background(), "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "unexpected test origin") {
		t.Fatalf("expected redirect rejection, got %v", err)
	}
}

func TestCleanupStaleStagingRemovesOnlyOldOwnedDirectories(t *testing.T) {
	base := t.TempDir()
	now := time.Date(2026, 9, 30, 5, 0, 0, 0, time.UTC)
	oldOwned := filepath.Join(base, "pxgo-update-old")
	recentOwned := filepath.Join(base, "pxgo-update-recent")
	unrelated := filepath.Join(base, "other-old")
	for _, path := range []string{oldOwned, recentOwned, unrelated} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := now.Add(-48 * time.Hour)
	if err := os.Chtimes(oldOwned, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(unrelated, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(recentOwned, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := CleanupStaleStaging(base, 24*time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldOwned); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old owned staging still exists: %v", err)
	}
	for _, path := range []string{recentOwned, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unexpected removal of %s: %v", path, err)
		}
	}
}

func TestValidateDownloadURLPinnedOrigins(t *testing.T) {
	if err := ValidateDownloadURL("https://github.com/khanhkit/pxgo/releases/download/v1.0.0/pxgo_linux_amd64.tar.gz", false); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDownloadURL("https://release-assets.githubusercontent.com/github-production-release-asset/foo", true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		raw      string
		redirect bool
	}{
		{"http://github.com/khanhkit/pxgo/releases/download/v1.0.0/x", false},
		{"https://github.com/other/pxgo/releases/download/v1.0.0/x", false},
		{"https://example.com/x", true},
	} {
		if err := ValidateDownloadURL(tc.raw, tc.redirect); err == nil {
			t.Fatalf("accepted %s", tc.raw)
		}
	}
}

func testOriginValidator(t *testing.T, base string) URLValidator {
	t.Helper()
	want, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	return func(raw string, _ bool) error {
		u, parseErr := url.Parse(raw)
		if parseErr != nil {
			return parseErr
		}
		if u.Scheme != want.Scheme || u.Host != want.Host {
			return fmt.Errorf("unexpected test origin %s", u.Host)
		}
		return nil
	}
}

func makeCandidateArchive(t *testing.T, assetName, binaryName string, payload []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if strings.HasSuffix(assetName, ".zip") {
		writer := zip.NewWriter(&out)
		entry, err := writer.Create(binaryName)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	gz := gzip.NewWriter(&out)
	tarWriter := tar.NewWriter(gz)
	if err := tarWriter.WriteHeader(&tar.Header{Name: binaryName, Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

var osReadFile = func(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}
