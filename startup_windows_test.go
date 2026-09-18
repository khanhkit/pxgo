//go:build windows

package main

import (
	"errors"
	"testing"

	"github.com/pavelsimo/pxgo/internal/winstartup"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type registryStringBackup struct {
	exists bool
	value  string
	kind   uint32
}

func readRegistryStringBackup(t *testing.T, key registry.Key, name string) registryStringBackup {
	t.Helper()
	value, kind, err := key.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return registryStringBackup{}
	}
	if err != nil {
		t.Fatal(err)
	}
	if kind != registry.SZ && kind != registry.EXPAND_SZ {
		t.Skipf("registry value %s has unsupported pre-existing type %d", name, kind)
	}
	return registryStringBackup{exists: true, value: value, kind: kind}
}

func restoreRegistryString(t *testing.T, key registry.Key, name string, backup registryStringBackup) {
	t.Helper()
	if !backup.exists {
		if err := key.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
			t.Errorf("delete %s during restore: %v", name, err)
		}
		return
	}
	var err error
	if backup.kind == registry.EXPAND_SZ {
		err = key.SetExpandStringValue(name, backup.value)
	} else {
		err = key.SetStringValue(name, backup.value)
	}
	if err != nil {
		t.Errorf("restore %s: %v", name, err)
	}
}

func TestAPISS0011RegistryInstallForceUninstallNative(t *testing.T) {
	key, existed, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}

	pxGoBackup := readRegistryStringBackup(t, key, winstartup.RegistryValueName)
	legacyBackup := readRegistryStringBackup(t, key, "Px")
	t.Cleanup(func() {
		restoreRegistryString(t, key, winstartup.RegistryValueName, pxGoBackup)
		restoreRegistryString(t, key, "Px", legacyBackup)
		_ = key.Close()
		if !existed {
			_ = registry.DeleteKey(registry.CURRENT_USER, runKeyPath)
		}
	})

	if err := key.SetStringValue("Px", "legacy-px-entry"); err != nil {
		t.Fatal(err)
	}
	_ = key.DeleteValue(winstartup.RegistryValueName)

	first := `"C:\Program Files\PxGo\pxgo.exe" "--config=C:\Users\Test User\pxgo.ini"`
	second := `"C:\Program Files\PxGo\pxgo.exe" "--config=C:\Users\Test User\other.ini"`

	if err := installStartup(first, false); err != nil {
		t.Fatal(err)
	}
	got, kind, err := key.GetStringValue(winstartup.RegistryValueName)
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("installed command=%q want=%q", got, first)
	}
	if kind != registry.SZ {
		t.Fatalf("registry kind=%d want REG_SZ", kind)
	}

	if err := installStartup(second, false); err != nil {
		t.Fatal(err)
	}
	got, _, err = key.GetStringValue(winstartup.RegistryValueName)
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("non-force install changed command to %q", got)
	}

	if err := installStartup(second, true); err != nil {
		t.Fatal(err)
	}
	got, _, err = key.GetStringValue(winstartup.RegistryValueName)
	if err != nil {
		t.Fatal(err)
	}
	if got != second {
		t.Fatalf("force install command=%q want=%q", got, second)
	}

	if err := uninstallStartup(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := key.GetStringValue(winstartup.RegistryValueName); !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("PxGo value still present after uninstall: %v", err)
	}
	legacy, _, err := key.GetStringValue("Px")
	if err != nil {
		t.Fatal(err)
	}
	if legacy != "legacy-px-entry" {
		t.Fatalf("legacy Px entry changed: %q", legacy)
	}
}

func TestAPISS0011RunCommandRoundTripsViaWindowsParser(t *testing.T) {
	exe := `C:\Program Files\PxGo & Tools\pxgo.exe`
	cfg := `C:\Users\Test User\Config^&%TEMP%\pxgo.ini`
	got, err := winstartup.BuildRunCommand(exe, cfg, func(path string) bool {
		return path == exe || path == cfg
	})
	if err != nil {
		t.Fatal(err)
	}

	args, err := windows.DecomposeCommandLine(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 {
		t.Fatalf("args=%q", args)
	}
	if args[0] != exe || args[1] != "--config="+cfg {
		t.Fatalf("round-trip args=%q", args)
	}
}
