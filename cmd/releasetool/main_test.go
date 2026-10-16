package main

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
)

func TestChecksums(t *testing.T) {
	dir := t.TempDir()
	for _, target := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"} {
		name := "agy-swap_v2.0.0_" + target
		if err := os.WriteFile(filepath.Join(dir, name), []byte(target), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	checksums := filepath.Join(dir, "checksums.txt")
	if err := writeChecksums(dir, checksums); err != nil {
		t.Fatal(err)
	}
	parsed, err := readChecksums(checksums)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 4 {
		t.Fatalf("expected 4 checksum entries, got %d", len(parsed))
	}
}

func TestVerifyVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "install.sh")
	if err := os.WriteFile(path, []byte("VERSION=\"${AGY_SWAP_VERSION:-2.0.0}\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyVersion("2.0.0", path); err != nil {
		t.Fatal(err)
	}
	if err := verifyVersion("2.1.0", path); err == nil {
		t.Fatal("expected version mismatch")
	}
	powershell := filepath.Join(dir, "install.ps1")
	if err := os.WriteFile(powershell, []byte("if ($true) { $Version = '2.0.0' }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyVersion("v2.0.0", powershell); err != nil {
		t.Fatalf("PowerShell installer version was not recognized: %v", err)
	}
}

func TestVerifyAssetsRequiresAllReleasePlatforms(t *testing.T) {
	dir := t.TempDir()
	for _, target := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64", "windows_amd64.exe", "windows_arm64.exe"} {
		if err := os.WriteFile(filepath.Join(dir, "agy-swap_v2.0.0_"+target), []byte(target), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeChecksums(dir, filepath.Join(dir, "checksums.txt")); err != nil {
		t.Fatal(err)
	}
	if err := verifyAssets("2.0.0", dir); err != nil {
		t.Fatal(err)
	}
	asset := filepath.Join(dir, "agy-swap_v2.0.0_linux_amd64")
	if err := os.WriteFile(asset, []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := verifyAssets("2.0.0", dir); err == nil {
		t.Fatal("tampered release asset accepted")
	}
	if err := writeChecksums(dir, filepath.Join(dir, "checksums.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "agy-swap_v2.0.0_windows_arm64.exe")); err != nil {
		t.Fatal(err)
	}
	if err := verifyAssets("2.0.0", dir); err == nil {
		t.Fatal("missing Windows asset was accepted")
	}
	t.Run("build provenance", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/custom\r\ngo 1.27.2\r\ntoolchain go1.27.3\r\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		module, floor, err := readModuleFloor(root)
		if err != nil || module != "example.com/custom" || floor != "go1.27.3" {
			t.Fatalf("readModuleFloor = %q, %q, %v", module, floor, err)
		}
		info := &debug.BuildInfo{GoVersion: "go1.27.3", Main: debug.Module{Path: module, Version: "(devel)"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "tagged-commit"}, {Key: "vcs.modified", Value: "false"}}}
		if err := checkBuildInfo(info, module, floor, "tagged-commit"); err != nil {
			t.Fatal(err)
		}
		info.GoVersion = "go1.27.2"
		if err := checkBuildInfo(info, module, floor, "tagged-commit"); err == nil {
			t.Fatal("outdated toolchain accepted")
		}
		info.GoVersion = "go1.27.3"
		info.Settings[1].Value = "true"
		if err := checkBuildInfo(info, module, floor, "tagged-commit"); err == nil {
			t.Fatal("dirty build accepted as release")
		}
		info.Settings[1].Value = "false"
		info.Main.Path = "example.com/other"
		if err := checkBuildInfo(info, module, floor, "tagged-commit"); err == nil {
			t.Fatal("different module accepted")
		}
	})

}

func TestCalculateNextVersion(t *testing.T) {
	for _, tc := range []struct {
		current, target, want string
	}{
		{"2.2.0", "patch", "2.2.1"},
		{"2.2.0", "minor", "2.3.0"},
		{"2.2.0", "major", "3.0.0"},
		{"2.2.0", "2.2.5", "2.2.5"},
		{"2.2.0", "v2.2.5", "2.2.5"},
	} {
		got, err := calculateNextVersion(tc.current, tc.target)
		if err != nil {
			t.Fatalf("calculateNextVersion(%q, %q) returned error: %v", tc.current, tc.target, err)
		}
		if got != tc.want {
			t.Fatalf("calculateNextVersion(%q, %q) = %q, want %q", tc.current, tc.target, got, tc.want)
		}
	}

	for _, invalid := range []string{"invalid", "2.2", "2.2.0.1", "foo.bar.baz"} {
		if _, err := calculateNextVersion("2.2.0", invalid); err == nil {
			t.Fatalf("expected error for invalid target %q", invalid)
		}
	}
}
