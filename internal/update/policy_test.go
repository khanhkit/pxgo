package update

import "testing"

func TestPolicyParsers(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  AutoMode
	}{
		{"", AutoOff},
		{"OFF", AutoOff},
		{"notify", AutoNotify},
		{"install", AutoInstall},
	} {
		got, err := ParseAutoMode(tc.input)
		if err != nil || got != tc.want {
			t.Fatalf("ParseAutoMode(%q)=%q,%v want %q", tc.input, got, err, tc.want)
		}
	}
	if _, err := ParseAutoMode("always"); err == nil {
		t.Fatal("accepted unsupported auto-update mode")
	}

	if got, err := ParseChannel(""); err != nil || got != Stable {
		t.Fatalf("default channel=%q,%v", got, err)
	}
	if got, err := ParseChannel("prerelease"); err != nil || got != Prerelease {
		t.Fatalf("prerelease channel=%q,%v", got, err)
	}
	if _, err := ParseChannel("nightly"); err == nil {
		t.Fatal("accepted unsupported channel")
	}

	if got, err := ParseProvider(""); err != nil || got != ProviderAuto {
		t.Fatalf("default provider=%q,%v", got, err)
	}
	if _, err := ParseProvider("apt"); err == nil {
		t.Fatal("accepted unsupported provider")
	}
}

func TestResolveProviderOwnership(t *testing.T) {
	tests := []struct {
		name       string
		configured Provider
		goos       string
		executable string
		want       Provider
	}{
		{"explicit direct portable", ProviderDirect, "windows", `C:\Tools\pxgo\pxgo.exe`, ProviderDirect},
		{"winget", ProviderAuto, "windows", `C:\Users\me\AppData\Local\Microsoft\WinGet\Packages\KhanhKit.PxGo_foo\pxgo.exe`, ProviderWinGet},
		{"scoop", ProviderAuto, "windows", `C:\Users\me\scoop\apps\pxgo\current\pxgo.exe`, ProviderScoop},
		{"homebrew arm", ProviderAuto, "darwin", `/opt/homebrew/Cellar/pxgo/1.2.3/bin/pxgo`, ProviderBrew},
		{"homebrew intel", ProviderAuto, "darwin", `/usr/local/Cellar/pxgo/1.2.3/bin/pxgo`, ProviderBrew},
		{"linuxbrew", ProviderAuto, "linux", `/home/linuxbrew/.linuxbrew/Cellar/pxgo/1.2.3/bin/pxgo`, ProviderBrew},
		{"portable", ProviderAuto, "linux", `/home/me/bin/pxgo`, ProviderDirect},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveProvider(tc.configured, tc.executable, tc.goos)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("provider=%q want %q", got, tc.want)
			}
		})
	}
}

func TestResolveProviderRejectsOwnershipConflict(t *testing.T) {
	for _, tc := range []struct {
		configured Provider
		goos       string
		executable string
	}{
		{ProviderDirect, "windows", `C:\Users\me\scoop\apps\pxgo\current\pxgo.exe`},
		{ProviderWinGet, "windows", `C:\Users\me\scoop\apps\pxgo\current\pxgo.exe`},
		{ProviderDirect, "darwin", `/opt/homebrew/Cellar/pxgo/1.2.3/bin/pxgo`},
	} {
		if _, err := ResolveProvider(tc.configured, tc.executable, tc.goos); err == nil {
			t.Fatalf("accepted provider %q for managed executable %q", tc.configured, tc.executable)
		}
	}
}

func TestResolveProviderAmbiguousManagerPathFailsClosed(t *testing.T) {
	if _, err := ResolveProvider(ProviderAuto, `C:\Users\me\scoop\shims\pxgo.exe`, "windows"); err == nil {
		t.Fatal("ambiguous Scoop-like path fell through to direct")
	}
	if got, err := ResolveProvider(ProviderScoop, `C:\Users\me\scoop\shims\pxgo.exe`, "windows"); err != nil || got != ProviderScoop {
		t.Fatalf("explicit Scoop marker=%q,%v", got, err)
	}
	if _, err := ResolveProvider(ProviderDirect, `/opt/homebrew/Cellar/custom/1.0/bin/pxgo`, "darwin"); err == nil {
		t.Fatal("manager-like Homebrew path accepted direct ownership")
	}
}

func TestResolveProviderAutoRequiresExecutable(t *testing.T) {
	if _, err := ResolveProvider(ProviderAuto, "", "linux"); err == nil {
		t.Fatal("expected empty executable path failure")
	}
}
