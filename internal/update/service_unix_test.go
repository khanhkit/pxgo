//go:build !windows

package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestServiceDirectUpdateStagesVerifiesAndActivates(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "pxgo")
	writeExecutable(t, target, "1.0.0")
	candidateScript := []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 1.1.0; exit 0; fi\nexit 1\n")
	assetName := AssetName(runtime.GOOS, runtime.GOARCH, "1.1.0")
	archive := makeCandidateArchive(t, assetName, productName, candidateScript)
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
					{"name": checksumsAssetName, "browser_download_url": base + "/checksums"},
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
	wantOrigin, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	validator := func(raw string, _ bool) error {
		u, parseErr := url.Parse(raw)
		if parseErr != nil {
			return parseErr
		}
		if u.Scheme != wantOrigin.Scheme || u.Host != wantOrigin.Host {
			return fmt.Errorf("unexpected origin %s", u.Host)
		}
		return nil
	}

	service := Service{
		Checker:    Checker{APIBase: srv.URL, Client: srv.Client()},
		Stager:     Stager{Client: srv.Client(), ValidateURL: validator, TempDir: t.TempDir()},
		Provider:   ProviderDirect,
		Channel:    Stable,
		Executable: target,
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
	}
	status, err := service.Update(context.Background(), "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Applied || status.Deferred || status.Provider != ProviderDirect {
		t.Fatalf("unexpected direct update status: %+v", status)
	}
	updated, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "1.1.0") {
		t.Fatalf("target not activated: %q", updated)
	}
}
