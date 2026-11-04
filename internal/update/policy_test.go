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

func TestAutoProviderRequiresCanonicalRootAndPackageIdentity(t *testing.T) {
	invalid := []struct {
		goos string
		file string
	}{
		{"windows", "C:/Temp/scoop/apps/pxgo/current/pxgo.exe"},
		{"windows", "C:/Users/me/scoop/apps/pxgo/current/other.exe"},
		{"windows", "C:/Users/me/scoop/apps/pxgo-copy/current/pxgo.exe"},
		{"windows", "C:/Temp/WinGet/Packages/KhanhKit.PxGo_unverified/pxgo.exe"},
		{"windows", "C:/Users/me/AppData/Local/Microsoft/WinGet/Packages/OtherApp_unverified/pxgo.exe"},
		{"windows", "C:/Users/me/AppData/Local/Microsoft/WinGet/Packages/KhanhKit.PxGo_unverified/other.exe"},
		{"windows", "C:/Users/me/AppData/Local/Microsoft/WinGet/Packages/KhanhKit.PxGo_/pxgo.exe"},
		{"darwin", "/tmp/Cellar/pxgo/0.9.0/bin/pxgo"},
		{"darwin", "/opt/homebrew/Cellar/pxgo-copy/0.9.0/bin/pxgo"},
		{"linux", "/home/me/fake/Cellar/pxgo/0.9.0/bin/pxgo"},
	}
	for _, tc := range invalid {
		t.Run(tc.file, func(t *testing.T) {
			provider, err := ResolveProvider(ProviderAuto, tc.file, tc.goos)
			if err == nil {
				t.Fatalf("ambiguous manager-shaped path trusted as %q", provider)
			}
		})
	}
}

func TestKnownAndExplicitCustomPackageManagerRoots(t *testing.T) {
	for _, tc := range []struct {
		name, goos, file string
		want             Provider
	}{
		{"global scoop", "windows", "C:/ProgramData/scoop/apps/pxgo/current/pxgo.exe", ProviderScoop},
		{"machine winget", "windows", "C:/Program Files/WinGet/Packages/KhanhKit.PxGo_foo/pxgo.exe", ProviderWinGet},
		{"machine winget x86", "windows", "C:/Program Files (x86)/WinGet/Packages/KhanhKit.PxGo_foo/pxgo.exe", ProviderWinGet},
		{"win user", "windows", "C:/Users/me/AppData/Local/Microsoft/WinGet/Packages/KhanhKit.PxGo_foo/pxgo.exe", ProviderWinGet},
		{"mac brew", "darwin", "/opt/homebrew/Cellar/pxgo/0.9.0/bin/pxgo", ProviderBrew},
		{"linuxbrew", "linux", "/home/linuxbrew/.linuxbrew/Cellar/pxgo/0.9.0/bin/pxgo", ProviderBrew},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveProvider(ProviderAuto, tc.file, tc.goos)
			if err != nil || got != tc.want {
				t.Fatalf("got=%q err=%v want=%q", got, err, tc.want)
			}
		})
	}

	t.Setenv("SCOOP", "D:/Installed/ScoopRoot")
	if got, err := ResolveProvider(ProviderAuto,
		"D:/Installed/ScoopRoot/apps/pxgo/current/pxgo.exe", "windows"); err != nil || got != ProviderScoop {
		t.Fatalf("explicit Scoop root not recognized: %q %v", got, err)
	}
	t.Setenv("HOMEBREW_PREFIX", "/opt/custombrew")
	if got, err := ResolveProvider(ProviderAuto,
		"/opt/custombrew/Cellar/pxgo/0.9.0/bin/pxgo", "darwin"); err != nil || got != ProviderBrew {
		t.Fatalf("explicit Homebrew prefix not recognized: %q %v", got, err)
	}
}

func TestResolveProviderExplicitWingetInsideScoopNamedProfile(t *testing.T) {
	file := `C:\Users\scoop\AppData\Local\Microsoft\WinGet\Packages\KhanhKit.PxGo_test\pxgo.exe`
	if detected, err := ResolveProvider(ProviderAuto, file, "windows"); err != nil || detected != ProviderWinGet {
		t.Fatalf("auto should recognize WinGet: %q %v", detected, err)
	}
	if explicit, err := ResolveProvider(ProviderWinGet, file, "windows"); err != nil || explicit != ProviderWinGet {
		t.Fatalf("explicit WinGet must preserve recognized ownership: %q %v", explicit, err)
	}
}

func TestResolveProviderUnixCaseSensitiveBrewRoot(t *testing.T) {
	file := `/OPT/HOMEBREW/Cellar/pxgo/0.9.0/bin/pxgo`
	if detected, err := ResolveProvider(ProviderAuto, file, "linux"); err == nil && detected == ProviderBrew {
		t.Fatalf("case-sensitive Linux root is not the official /opt/homebrew: %q", detected)
	}
}

func TestResolveProviderPortableManagerNamedProfileIsDirect(t *testing.T) {
	for _, file := range []string{
		`C:\Users\scoop\Downloads\pxgo.exe`,
		`C:\Users\winget\Desktop\pxgo.exe`,
	} {
		for _, configured := range []Provider{ProviderAuto, ProviderDirect} {
			got, err := ResolveProvider(configured, file, "windows")
			if err != nil || got != ProviderDirect {
				t.Errorf("manager name in username is not an install owner: file=%q configured=%q got=%q err=%v", file, configured, got, err)
			}
		}
	}
}

func TestResolveProviderPortableCellarNamedProfileIsDirect(t *testing.T) {
	file := "/Users/Cellar/Downloads/pxgo"
	for _, configured := range []Provider{ProviderAuto, ProviderDirect} {
		got, err := ResolveProvider(configured, file, "darwin")
		if err != nil || got != ProviderDirect {
			t.Errorf("plain user named Cellar should not mark Homebrew: got=%q err=%v", got, err)
		}
	}
}

func TestResolveProviderNonPxGoManagerShapedPathsStayDirect(t *testing.T) {
	for _, tc := range []struct {
		goos string
		file string
	}{
		{"darwin", "/tmp/Cellar/other/1.0/bin/pxgo"},
		{"linux", "/opt/sandbox/Cellar/notpxgo/1.0/bin/pxgo"},
		{"windows", "C:/tmp/not-scoop/apps/other/current/pxgo.exe"},
		{"windows", "C:/tmp/scoop/apps/other/current/pxgo.exe"},
		{"windows", "C:/tmp/winget/packages/OtherApp_1.0/pxgo.exe"},
		{"windows", "C:/tmp/not-winget/packages/KhanhKit.PxGo_test/pxgo.exe"},
	} {
		for _, configured := range []Provider{ProviderAuto, ProviderDirect} {
			got, err := ResolveProvider(configured, tc.file, tc.goos)
			if err != nil || got != ProviderDirect {
				t.Errorf("unrelated manager-shaped path must remain portable: %q %q got=%q err=%v", tc.goos, tc.file, got, err)
			}
		}
	}
}

func TestResolveProviderPortableFalsePositiveManagerHints(t *testing.T) {
	cases := []struct{ goos, exe string }{
		{"windows", `C:\tmp\scoop\shims\pxgo.exe`},
		{"windows", `C:\tmp\winget\links\pxgo.exe`},
		{"windows", `C:\tmp\winget\packages\khanhkit.pxgoevil\pxgo.exe`},
		{"windows", `C:\tmp\winget\packages\khanhkit.pxgo_\pxgo.exe`},
	}
	for _, tc := range cases {
		for _, configured := range []Provider{ProviderAuto, ProviderDirect} {
			got, err := ResolveProvider(configured, tc.exe, tc.goos)
			if err != nil || got != ProviderDirect {
				t.Errorf("false manager hint: %q %s got=%q err=%v", tc.exe, configured, got, err)
			}
		}
	}
}

func TestResolveProviderCanonicalManagerShimsRemainAmbiguous(t *testing.T) {
	for _, tc := range []struct {
		goos, executable string
		provider         Provider
	}{
		{"windows", "C:/Users/me/scoop/shims/pxgo.exe", ProviderScoop},
		{"windows", "C:/Users/me/AppData/Local/Microsoft/WinGet/Links/pxgo.exe", ProviderWinGet},
	} {
		if _, err := ResolveProvider(ProviderAuto, tc.executable, tc.goos); err == nil {
			t.Errorf("canonical manager shim must remain ambiguous: %q", tc.executable)
		}
		if got, err := ResolveProvider(tc.provider, tc.executable, tc.goos); err != nil || got != tc.provider {
			t.Errorf("explicit canonical manager owner: %q got=%q err=%v", tc.executable, got, err)
		}
	}
}
