package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServiceManagerUpdateDelegatesOnlyWhenAvailable(t *testing.T) {
	latest := "v1.1.0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"tag_name":%q,"draft":false,"prerelease":false,"assets":[]}`, latest)
	}))
	defer srv.Close()
	runner := &captureRunner{}
	service := Service{
		Checker:    Checker{APIBase: srv.URL, Client: srv.Client()},
		Runner:     runner,
		Provider:   ProviderScoop,
		Channel:    Stable,
		Executable: `/home/me/scoop/apps/pxgo/current/pxgo.exe`,
		GOOS:       "windows",
		VerifyInstalled: func(context.Context, string, string) error {
			return nil
		},
	}
	status, err := service.Update(context.Background(), "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Applied || status.Provider != ProviderScoop || runner.name != "scoop" {
		t.Fatalf("unexpected manager update: status=%+v runner=%s %v", status, runner.name, runner.args)
	}

	runner.name, runner.args = "", nil
	latest = "v1.0.0"
	status, err = service.Update(context.Background(), "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if status.Applied || runner.name != "" {
		t.Fatalf("no-update path executed manager: status=%+v runner=%s", status, runner.name)
	}
}

func TestServiceManagerUpdateRequiresInstalledVersionProof(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.1.0","draft":false,"prerelease":false,"assets":[]}`))
	}))
	defer srv.Close()
	runner := &captureRunner{}
	service := Service{
		Checker:    Checker{APIBase: srv.URL, Client: srv.Client()},
		Runner:     runner,
		Provider:   ProviderScoop,
		Channel:    Stable,
		Executable: `/home/me/scoop/apps/pxgo/current/pxgo.exe`,
		GOOS:       "windows",
		VerifyInstalled: func(context.Context, string, string) error {
			return fmt.Errorf("version stayed old")
		},
		LookPath: func(string) (string, error) {
			return "", fmt.Errorf("not found")
		},
	}
	status, err := service.Update(context.Background(), "1.0.0")
	if err == nil {
		t.Fatal("manager command exit zero without version proof was accepted")
	}
	if status.Applied {
		t.Fatalf("status unexpectedly applied: %+v", status)
	}
	if runner.name != "scoop" {
		t.Fatalf("provider command=%q", runner.name)
	}
}

func TestServiceCheckReportsResolvedProviderAndChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.1","draft":false,"prerelease":false,"assets":[]}`))
	}))
	defer srv.Close()
	service := Service{
		Checker:    Checker{APIBase: srv.URL, Client: srv.Client()},
		Provider:   ProviderAuto,
		Channel:    Stable,
		Executable: `/opt/homebrew/Cellar/pxgo/1.0.0/bin/pxgo`,
		GOOS:       "darwin",
	}
	status, err := service.Check(context.Background(), "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if status.Provider != ProviderBrew || status.Channel != Stable || !status.Available || status.Latest != "1.0.1" {
		t.Fatalf("unexpected check status: %+v", status)
	}
}
