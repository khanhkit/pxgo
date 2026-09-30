package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestCheckerStableNoDowngrade(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/khanhkit/pxgo/releases/latest" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v0.8.0","draft":false,"prerelease":false,"assets":[]}`))
	}))
	defer srv.Close()
	checker := Checker{APIBase: srv.URL, Client: srv.Client()}
	got, err := checker.Check(context.Background(), "0.9.0")
	if err != nil {
		t.Fatal(err)
	}
	if got.Available {
		t.Fatal("older release must not be offered")
	}
	if got.Latest != "0.8.0" || got.Current != "0.9.0" {
		t.Fatalf("got %+v", got)
	}
}

func TestCheckerRejectsPrereleaseFromStableEndpoint(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0-rc.1","draft":false,"prerelease":true,"assets":[]}`))
	}))
	defer srv.Close()
	_, err := (Checker{APIBase: srv.URL, Client: srv.Client()}).Check(context.Background(), "0.9.0")
	if err == nil {
		t.Fatal("expected stable channel rejection")
	}
}

func TestCheckerRejectsMalformedTag(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"latest","draft":false,"prerelease":false,"assets":[]}`))
	}))
	defer srv.Close()
	_, err := (Checker{APIBase: srv.URL, Client: srv.Client()}).Check(context.Background(), "0.9.0")
	if err == nil {
		t.Fatal("expected malformed tag rejection")
	}
}

type captureRunner struct {
	name string
	args []string
	err  error
}

func (r *captureRunner) Run(_ context.Context, name string, args ...string) error {
	r.name, r.args = name, append([]string(nil), args...)
	return r.err
}

func TestUpgradeDelegatesExactPackage(t *testing.T) {
	tests := []struct {
		provider Provider
		name     string
		args     []string
	}{
		{ProviderWinGet, "winget", []string{"upgrade", "--id", "KhanhKit.PxGo", "--exact", "--disable-interactivity", "--accept-source-agreements", "--accept-package-agreements"}},
		{ProviderScoop, "scoop", []string{"update", "pxgo"}},
		{ProviderBrew, "brew", []string{"upgrade", "pxgo"}},
	}
	for _, tc := range tests {
		t.Run(string(tc.provider), func(t *testing.T) {
			r := &captureRunner{}
			if err := Upgrade(context.Background(), tc.provider, r); err != nil {
				t.Fatal(err)
			}
			if r.name != tc.name || !reflect.DeepEqual(r.args, tc.args) {
				t.Fatalf("command=%s %#v", r.name, r.args)
			}
		})
	}
}

func TestUpgradeDirectFailsClosed(t *testing.T) {
	r := &captureRunner{}
	if err := Upgrade(context.Background(), ProviderDirect, r); err == nil {
		t.Fatal("direct update must require verified staging")
	}
	if r.name != "" {
		t.Fatal("direct update unexpectedly executed command")
	}
}

func TestUpgradePropagatesProviderFailure(t *testing.T) {
	r := &captureRunner{err: errors.New("boom")}
	if err := Upgrade(context.Background(), ProviderScoop, r); err == nil {
		t.Fatal("expected error")
	}
}

func TestVerifySHA256(t *testing.T) {
	payload := "trusted candidate"
	sum := sha256.Sum256([]byte(payload))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if err := VerifySHA256(strings.NewReader(payload), digest); err != nil {
		t.Fatal(err)
	}
	if err := VerifySHA256(strings.NewReader(payload+"x"), digest); err == nil {
		t.Fatal("accepted mismatched bytes")
	}
	if err := VerifySHA256(strings.NewReader(payload), "sha512:abcd"); err == nil {
		t.Fatal("accepted unsupported digest")
	}
}

func TestChecksumForAssetExactAndUnique(t *testing.T) {
	sum := strings.Repeat("a", 64)
	got, err := ChecksumForAsset(strings.NewReader(sum+"  pxgo.zip\n"), "pxgo.zip")
	if err != nil {
		t.Fatal(err)
	}
	if got != sum {
		t.Fatalf("checksum=%s", got)
	}
	if _, err := ChecksumForAsset(strings.NewReader(sum+"  pxgo.zip\n"+sum+"  pxgo.zip\n"), "pxgo.zip"); err == nil {
		t.Fatal("accepted duplicate checksum entry")
	}
	if _, err := ChecksumForAsset(strings.NewReader(sum+"  other.zip\n"), "pxgo.zip"); err == nil {
		t.Fatal("accepted missing exact filename")
	}
}

func TestValidateAssetURLPinnedRepository(t *testing.T) {
	if err := ValidateAssetURL("https://github.com/khanhkit/pxgo/releases/download/v1.0.0/pxgo.zip"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"http://github.com/khanhkit/pxgo/releases/download/v1.0.0/pxgo.zip",
		"https://example.com/khanhkit/pxgo/releases/download/v1.0.0/pxgo.zip",
		"https://github.com/other/pxgo/releases/download/v1.0.0/pxgo.zip",
	} {
		if err := ValidateAssetURL(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	if CompareVersions("1.2.3", "1.2.2") <= 0 {
		t.Fatal("newer not detected")
	}
	if CompareVersions("1.2.3", "1.2.3") != 0 {
		t.Fatal("equal mismatch")
	}
	if CompareVersions("1.2.3", "2.0.0") >= 0 {
		t.Fatal("older mismatch")
	}
	if CompareVersions("1.0.0-rc.1", "1.0.0") >= 0 {
		t.Fatal("prerelease must sort before final release")
	}
	if CompareVersions("1.0.0-rc.2", "1.0.0-rc.1") <= 0 {
		t.Fatal("prerelease numeric identifiers not ordered")
	}
	if CompareVersions("1.0.0-beta", "1.0.0-1") <= 0 {
		t.Fatal("numeric prerelease identifier must sort before text")
	}
}

func TestAssetNameMatchesGoReleaserContract(t *testing.T) {
	if got := AssetName("windows", "amd64", "1.2.3"); got != "pxgo_windows_amd64.zip" {
		t.Fatalf("windows asset=%q", got)
	}
	if got := AssetName("linux", "arm64", "1.2.3"); got != "pxgo_linux_arm64.tar.gz" {
		t.Fatalf("linux asset=%q", got)
	}
}

func TestPrereleaseChannelSelectsHighestAcceptedRelease(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"tag_name":"v1.0.0-rc.1","draft":false,"prerelease":true,"assets":[]},
			{"tag_name":"v1.0.0","draft":false,"prerelease":false,"assets":[]},
			{"tag_name":"v0.9.9","draft":false,"prerelease":false,"assets":[]}
		]`))
	}))
	defer srv.Close()
	got, err := (Checker{APIBase: srv.URL, Client: srv.Client(), Channel: Prerelease}).Check(context.Background(), "0.9.0")
	if err != nil {
		t.Fatal(err)
	}
	if got.Latest != "1.0.0" {
		t.Fatalf("latest=%q", got.Latest)
	}
}
