package main

import (
	"bufio"
	"debug/buildinfo"
	"errors"
	"fmt"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
)

var releaseTargets = []string{
	"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64",
	"windows_amd64.exe", "windows_arm64.exe",
}

// readModuleFloor returns the module path and the minimum release toolchain:
// the newer of go.mod's go directive and its optional toolchain line.
func readModuleFloor(root string) (string, string, error) {
	file, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", "", err
	}
	defer func() { _ = file.Close() }()
	var module, directive, toolchain string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "module":
			module = fields[1]
		case "go":
			directive = "go" + fields[1]
		case "toolchain":
			toolchain = fields[1]
		}
	}
	if err := scanner.Err(); err != nil {
		return "", "", err
	}
	if module == "" {
		return "", "", errors.New("go.mod declares no module path")
	}
	floor := directive
	if version.IsValid(toolchain) && version.Compare(toolchain, floor) > 0 {
		floor = toolchain
	}
	if !version.IsValid(floor) {
		return "", "", errors.New("go.mod must declare a Go version floor (go X.Y.Z)")
	}
	return module, floor, nil
}

// goRelease drops build suffixes such as " X:boringcrypto" from a Go version.
func goRelease(goVersion string) string {
	fields := strings.Fields(goVersion)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func checkToolchain(goVersion, floor string) error {
	if version.Compare(goRelease(goVersion), floor) < 0 {
		return fmt.Errorf("go toolchain %s is older than the release floor %s", goVersion, floor)
	}
	return nil
}

func gitOutput(root string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// verifySource refuses to build release assets unless HEAD is the clean,
// annotated release tag and the compiler meets the go.mod toolchain floor.
func verifySource(releaseVersion, root, goVersion string) error {
	tag := "v" + strings.TrimPrefix(releaseVersion, "v")
	_, floor, err := readModuleFloor(root)
	if err != nil {
		return err
	}
	if err = checkToolchain(goVersion, floor); err != nil {
		return err
	}
	status, err := gitOutput(root, "status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("release source has uncommitted or untracked changes:\n%s", status)
	}
	kind, err := gitOutput(root, "cat-file", "-t", "refs/tags/"+tag)
	if err != nil {
		return fmt.Errorf("release tag %s not found", tag)
	}
	if kind != "tag" {
		return fmt.Errorf("release tag %s must be annotated", tag)
	}
	tagged, err := gitOutput(root, "rev-parse", "refs/tags/"+tag+"^{commit}")
	if err != nil {
		return err
	}
	head, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if tagged != head {
		return fmt.Errorf("HEAD %s is not release tag %s (%s)", head, tag, tagged)
	}
	return nil
}

// checkBuildInfo accepts only a binary built from the clean release tag of the
// expected module with a toolchain at or above the floor.
func checkBuildInfo(info *debug.BuildInfo, module, floor, revision string) error {
	if info.Main.Path != module {
		return fmt.Errorf("main module %q, expected %q", info.Main.Path, module)
	}
	settings := make(map[string]string)
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["vcs.revision"] != revision || settings["vcs.modified"] != "false" {
		return fmt.Errorf("release requires clean revision %s; got revision %q, modified %q", revision, settings["vcs.revision"], settings["vcs.modified"])
	}
	return checkToolchain(info.GoVersion, floor)
}

func verifyBuilds(releaseVersion, dir, module, floor, revision string) error {
	for _, target := range releaseTargets {
		name := "agy-swap_v" + strings.TrimPrefix(releaseVersion, "v") + "_" + target
		info, err := buildinfo.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := checkBuildInfo(info, module, floor, revision); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// verifyExperiment validates local build identity without requiring a release tag
// or pretending that an experimental worktree is a clean release.
func verifyExperiment(releaseVersion, buildID, binary, root string) error {
	module, floor, err := readModuleFloor(root)
	if err != nil {
		return err
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		return err
	}
	if info.Main.Path != module {
		return fmt.Errorf("main module %q, expected %q", info.Main.Path, module)
	}
	if err := checkToolchain(info.GoVersion, floor); err != nil {
		return err
	}
	settings := make(map[string]string)
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["GOOS"] != "windows" || settings["-trimpath"] != "true" {
		return errors.New("experimental binary must target Windows and use -trimpath")
	}
	head, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if settings["vcs.revision"] != head {
		return fmt.Errorf("binary revision %q differs from HEAD %s", settings["vcs.revision"], head)
	}
	status, err := gitOutput(root, "status", "--porcelain")
	if err != nil {
		return err
	}
	if settings["vcs.modified"] != fmt.Sprint(status != "") {
		return errors.New("binary worktree status differs from source")
	}
	output, err := exec.Command(binary, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("read binary version: %w", err)
	}
	text := string(output)
	if strings.TrimSpace(text) != fmt.Sprintf("agy-swap v%s (%s)", releaseVersion, buildID) {
		return fmt.Errorf("unexpected experimental binary identity: %s", text)
	}
	return nil
}
