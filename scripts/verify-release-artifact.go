//go:build ignore

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func main() {
	var dist, targetOS, targetArch, expectedVersion string
	flag.StringVar(&dist, "dist", "dist", "candidate dist directory")
	flag.StringVar(&targetOS, "os", runtime.GOOS, "target OS")
	flag.StringVar(&targetArch, "arch", runtime.GOARCH, "target architecture")
	flag.StringVar(&expectedVersion, "version", "", "expected release version without leading v")
	flag.Parse()

	if err := verify(dist, targetOS, targetArch, expectedVersion); err != nil {
		fmt.Fprintln(os.Stderr, "release-artifact verification failed:", err)
		os.Exit(1)
	}
	fmt.Printf("release-artifact verification passed for %s/%s\n", targetOS, targetArch)
}

func verify(dist, targetOS, targetArch, expectedVersion string) error {
	ext := ".tar.gz"
	binaryName := "pxgo"
	if targetOS == "windows" {
		ext = ".zip"
		binaryName = "pxgo.exe"
	}
	archiveName := fmt.Sprintf("pxgo_%s_%s%s", targetOS, targetArch, ext)
	archivePath := filepath.Join(dist, archiveName)

	if err := verifyChecksum(filepath.Join(dist, "checksums.txt"), archiveName, archivePath); err != nil {
		return err
	}

	extractDir, err := os.MkdirTemp("", "pxgo-release-artifact-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(extractDir)

	if targetOS == "windows" {
		err = extractZip(archivePath, extractDir)
	} else {
		err = extractTarGz(archivePath, extractDir)
	}
	if err != nil {
		return err
	}

	binaryPath, err := findFile(extractDir, binaryName)
	if err != nil {
		return err
	}
	if targetOS != "windows" {
		if err := os.Chmod(binaryPath, 0o755); err != nil {
			return fmt.Errorf("chmod candidate binary: %w", err)
		}
	} else {
		backgroundPath, err := findFile(extractDir, "pxgow.exe")
		if err != nil {
			return fmt.Errorf("windowless companion: %w", err)
		}
		for _, candidate := range []struct {
			path      string
			name      string
			subsystem uint16
		}{
			{path: binaryPath, name: "pxgo.exe", subsystem: 3},
			{path: backgroundPath, name: "pxgow.exe", subsystem: 2},
		} {
			if err := verifyWindowsSubsystem(candidate.path, candidate.subsystem); err != nil {
				return fmt.Errorf("%s subsystem: %w", candidate.name, err)
			}
			if err := verifyWindowsIcon(candidate.path, filepath.Join("assets", "windows", "pxgo.ico")); err != nil {
				return fmt.Errorf("%s icon: %w", candidate.name, err)
			}
		}
	}

	versionOut, err := exec.Command(binaryPath, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("--version: %w: %s", err, versionOut)
	}
	gotVersion := strings.TrimSpace(string(versionOut))
	if gotVersion == "" || gotVersion == "dev" {
		return fmt.Errorf("invalid release version %q", gotVersion)
	}
	if expectedVersion != "" && gotVersion != expectedVersion {
		return fmt.Errorf("version=%q want %q", gotVersion, expectedVersion)
	}

	helpOut, err := exec.Command(binaryPath, "--help").CombinedOutput()
	if err != nil {
		return fmt.Errorf("--help: %w: %s", err, helpOut)
	}
	if !strings.Contains(string(helpOut), "Usage:") {
		return errors.New("--help output missing Usage")
	}

	if err := smokeProxy(binaryPath); err != nil {
		return err
	}
	if targetOS == "windows" {
		backgroundPath, err := findFile(extractDir, "pxgow.exe")
		if err != nil {
			return err
		}
		if err := smokeBackground(binaryPath, backgroundPath); err != nil {
			return err
		}
	}
	return nil
}

func verifyChecksum(checksumPath, archiveName, archivePath string) error {
	data, err := os.ReadFile(checksumPath)
	if err != nil {
		return fmt.Errorf("read checksums: %w", err)
	}
	want := ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimPrefix(fields[len(fields)-1], "*") == archiveName {
			want = fields[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("checksum entry missing for %s", archiveName)
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("archive checksum=%s want=%s", got, want)
	}
	return nil
}

const windowsIconSHA256 = "d84be2b1f38218675a6fc74693826ac3920ce576c4b749d87599230bbbb6208f"

func verifyWindowsSubsystem(binaryPath string, want uint16) error {
	f, err := pe.Open(binaryPath)
	if err != nil {
		return fmt.Errorf("open PE: %w", err)
	}
	defer f.Close()
	var got uint16
	switch header := f.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		got = header.Subsystem
	case *pe.OptionalHeader64:
		got = header.Subsystem
	default:
		return errors.New("PE optional header unavailable")
	}
	if got != want {
		return fmt.Errorf("got %d want %d", got, want)
	}
	return nil
}

const (
	resourceTypeIcon      = 3
	resourceTypeGroupIcon = 14
)

type icoFrame struct {
	width      byte
	height     byte
	colorCount byte
	reserved   byte
	planes     uint16
	bitCount   uint16
	data       []byte
}

type resourceDirEntry struct {
	id     uint32
	named  bool
	isDir  bool
	offset uint32
}

func verifyWindowsIcon(binaryPath, iconPath string) error {
	iconData, err := os.ReadFile(iconPath)
	if err != nil {
		return fmt.Errorf("read source icon: %w", err)
	}
	digest := sha256.Sum256(iconData)
	if got := hex.EncodeToString(digest[:]); got != windowsIconSHA256 {
		return fmt.Errorf("source icon checksum=%s want=%s", got, windowsIconSHA256)
	}
	frames, err := parseICO(iconData)
	if err != nil {
		return fmt.Errorf("parse source icon: %w", err)
	}

	f, err := pe.Open(binaryPath)
	if err != nil {
		return fmt.Errorf("open PE: %w", err)
	}
	defer f.Close()
	var resourceSection *pe.Section
	for _, section := range f.Sections {
		if section.Name == ".rsrc" {
			resourceSection = section
			break
		}
	}
	if resourceSection == nil {
		return errors.New("PE has no .rsrc section")
	}
	resourceData, err := resourceSection.Data()
	if err != nil {
		return fmt.Errorf("read PE resources: %w", err)
	}
	icons, err := peResourcePayloads(resourceData, resourceSection.VirtualAddress, resourceTypeIcon)
	if err != nil {
		return fmt.Errorf("read RT_ICON resources: %w", err)
	}
	groups, err := peResourcePayloads(resourceData, resourceSection.VirtualAddress, resourceTypeGroupIcon)
	if err != nil {
		return fmt.Errorf("read RT_GROUP_ICON resources: %w", err)
	}
	if len(icons) == 0 || len(groups) == 0 {
		return fmt.Errorf("missing icon resources: icons=%d groups=%d", len(icons), len(groups))
	}
	var lastErr error
	for _, group := range groups {
		if err := verifyGroupIcon(group, frames, icons); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("no RT_GROUP_ICON matches source icon: %w", lastErr)
}

func parseICO(data []byte) ([]icoFrame, error) {
	if len(data) < 6 || binary.LittleEndian.Uint16(data[0:2]) != 0 || binary.LittleEndian.Uint16(data[2:4]) != 1 {
		return nil, errors.New("invalid ICO header")
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	if count == 0 || 6+count*16 > len(data) {
		return nil, errors.New("invalid ICO directory")
	}
	frames := make([]icoFrame, 0, count)
	for i := 0; i < count; i++ {
		off := 6 + i*16
		size := int(binary.LittleEndian.Uint32(data[off+8 : off+12]))
		imageOffset := int(binary.LittleEndian.Uint32(data[off+12 : off+16]))
		if size <= 0 || imageOffset < 0 || imageOffset > len(data) || size > len(data)-imageOffset {
			return nil, fmt.Errorf("ICO frame %d has invalid bounds", i)
		}
		frames = append(frames, icoFrame{
			width:      data[off],
			height:     data[off+1],
			colorCount: data[off+2],
			reserved:   data[off+3],
			planes:     binary.LittleEndian.Uint16(data[off+4 : off+6]),
			bitCount:   binary.LittleEndian.Uint16(data[off+6 : off+8]),
			data:       append([]byte(nil), data[imageOffset:imageOffset+size]...),
		})
	}
	return frames, nil
}

func peResourcePayloads(data []byte, sectionRVA uint32, typeID uint32) (map[uint32][]byte, error) {
	root, err := resourceDirEntries(data, 0)
	if err != nil {
		return nil, err
	}
	var typeDir *resourceDirEntry
	for i := range root {
		if !root[i].named && root[i].id == typeID && root[i].isDir {
			typeDir = &root[i]
			break
		}
	}
	if typeDir == nil {
		return map[uint32][]byte{}, nil
	}
	names, err := resourceDirEntries(data, typeDir.offset)
	if err != nil {
		return nil, err
	}
	payloads := make(map[uint32][]byte)
	for _, name := range names {
		if name.named || !name.isDir {
			continue
		}
		languages, err := resourceDirEntries(data, name.offset)
		if err != nil {
			return nil, err
		}
		for _, language := range languages {
			if language.isDir {
				continue
			}
			payload, err := resourceDataEntry(data, sectionRVA, language.offset)
			if err != nil {
				return nil, err
			}
			payloads[name.id] = payload
			break
		}
	}
	return payloads, nil
}

func resourceDirEntries(data []byte, offset uint32) ([]resourceDirEntry, error) {
	start := int(offset)
	if start < 0 || start > len(data) || 16 > len(data)-start {
		return nil, fmt.Errorf("resource directory offset %#x out of bounds", offset)
	}
	namedCount := int(binary.LittleEndian.Uint16(data[start+12 : start+14]))
	idCount := int(binary.LittleEndian.Uint16(data[start+14 : start+16]))
	count := namedCount + idCount
	if count < 0 || 8*count > len(data)-(start+16) {
		return nil, fmt.Errorf("resource directory at %#x has invalid entry count", offset)
	}
	entries := make([]resourceDirEntry, 0, count)
	for i := 0; i < count; i++ {
		off := start + 16 + i*8
		name := binary.LittleEndian.Uint32(data[off : off+4])
		target := binary.LittleEndian.Uint32(data[off+4 : off+8])
		entries = append(entries, resourceDirEntry{
			id:     name & 0xffff,
			named:  name&0x80000000 != 0,
			isDir:  target&0x80000000 != 0,
			offset: target & 0x7fffffff,
		})
	}
	return entries, nil
}

func resourceDataEntry(data []byte, sectionRVA, offset uint32) ([]byte, error) {
	start := int(offset)
	if start < 0 || start > len(data) || 16 > len(data)-start {
		return nil, fmt.Errorf("resource data entry offset %#x out of bounds", offset)
	}
	rva := binary.LittleEndian.Uint32(data[start : start+4])
	size := binary.LittleEndian.Uint32(data[start+4 : start+8])
	if rva < sectionRVA {
		return nil, fmt.Errorf("resource RVA %#x precedes .rsrc RVA %#x", rva, sectionRVA)
	}
	payloadOffset := uint64(rva - sectionRVA)
	payloadSize := uint64(size)
	if payloadOffset > uint64(len(data)) || payloadSize > uint64(len(data))-payloadOffset {
		return nil, fmt.Errorf("resource payload RVA %#x size %d out of bounds", rva, size)
	}
	return append([]byte(nil), data[payloadOffset:payloadOffset+payloadSize]...), nil
}

func verifyGroupIcon(group []byte, frames []icoFrame, icons map[uint32][]byte) error {
	if len(group) < 6 || binary.LittleEndian.Uint16(group[0:2]) != 0 || binary.LittleEndian.Uint16(group[2:4]) != 1 {
		return errors.New("invalid group icon header")
	}
	count := int(binary.LittleEndian.Uint16(group[4:6]))
	if count != len(frames) || 6+count*14 > len(group) {
		return fmt.Errorf("group icon count=%d want=%d", count, len(frames))
	}
	matched := make([]bool, len(frames))
	for i := 0; i < count; i++ {
		off := 6 + i*14
		resourceID := uint32(binary.LittleEndian.Uint16(group[off+12 : off+14]))
		payload, ok := icons[resourceID]
		if !ok {
			return fmt.Errorf("group icon references missing RT_ICON id=%d", resourceID)
		}
		bytesInResource := binary.LittleEndian.Uint32(group[off+8 : off+12])
		found := false
		for j, frame := range frames {
			if matched[j] || frame.width != group[off] || frame.height != group[off+1] || frame.colorCount != group[off+2] || frame.reserved != group[off+3] || frame.planes != binary.LittleEndian.Uint16(group[off+4:off+6]) || frame.bitCount != binary.LittleEndian.Uint16(group[off+6:off+8]) || uint32(len(frame.data)) != bytesInResource || !bytes.Equal(frame.data, payload) {
				continue
			}
			matched[j] = true
			found = true
			break
		}
		if !found {
			return fmt.Errorf("RT_ICON id=%d does not match any source ICO frame", resourceID)
		}
	}
	return nil
}

func extractTarGz(path, dest string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
}

func extractZip(path, dest string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		target, err := safeJoin(dest, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		in.Close()
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func safeJoin(root, name string) (string, error) {
	clean := filepath.Clean(name)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return filepath.Join(root, clean), nil
}

func findFile(root, name string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(d.Name(), name) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("%s not found in archive", name)
	}
	return found, nil
}

func smokeProxy(binaryPath string) error {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "artifact-ok")
	}))
	defer upstream.Close()

	port, err := freePort()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath,
		"--port="+strconv.Itoa(port),
		"--listen=127.0.0.1",
		"--proxy=DIRECT",
	)
	cmd.Env = append(os.Environ(), "PXGO_PROXY=DIRECT")
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start packaged proxy: %w", err)
	}

	if err := waitForPort(port, 8*time.Second); err != nil {
		cancel()
		_ = cmd.Wait()
		return fmt.Errorf("%w\n%s", err, output.String())
	}

	proxyURL := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 5 * time.Second}
	resp, err := client.Get(upstream.URL)
	if err != nil {
		cancel()
		_ = cmd.Wait()
		return fmt.Errorf("proxy smoke request: %w\n%s", err, output.String())
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil || resp.StatusCode != http.StatusOK || string(body) != "artifact-ok" {
		cancel()
		_ = cmd.Wait()
		return fmt.Errorf("proxy smoke response status=%s body=%q err=%v", resp.Status, body, readErr)
	}

	quitOut, err := exec.Command(binaryPath, "--port="+strconv.Itoa(port), "--quit").CombinedOutput()
	if err != nil {
		cancel()
		_ = cmd.Wait()
		return fmt.Errorf("packaged proxy quit: %w: %s", err, quitOut)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("packaged proxy exit: %w\n%s", err, output.String())
		}
	case <-time.After(8 * time.Second):
		cancel()
		return errors.New("packaged proxy did not stop after --quit")
	}
	return nil
}

func smokeBackground(binaryPath, backgroundPath string) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	args := []string{
		"--background",
		"--port=" + strconv.Itoa(port),
		"--listen=127.0.0.1",
		"--proxy=DIRECT",
	}
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append(os.Environ(), "PXGO_PROXY=DIRECT")
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("background launch: %w: %s", err, output.String())
	}
	if err := waitForPort(port, 8*time.Second); err != nil {
		return fmt.Errorf("background runtime readiness: %w", err)
	}

	processes, err := windowsPxGoProcesses(binaryPath, backgroundPath)
	if err != nil {
		return err
	}
	if processes.pxgo < 2 {
		return fmt.Errorf("background process tree pxgo.exe count=%d want at least 2 (Guardian parent + worker)", processes.pxgo)
	}
	if processes.pxgow != 1 {
		return fmt.Errorf("background tray host pxgow.exe count=%d want 1", processes.pxgow)
	}

	quitOut, err := exec.Command(binaryPath, "--port="+strconv.Itoa(port), "--quit").CombinedOutput()
	if err != nil {
		return fmt.Errorf("background proxy quit: %w: %s", err, quitOut)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		processes, err = windowsPxGoProcesses(binaryPath, backgroundPath)
		if err != nil {
			return err
		}
		if processes.pxgo == 0 && processes.pxgow == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if processes.pxgo != 0 || processes.pxgow != 0 {
		return fmt.Errorf("background quit left processes: pxgo=%d pxgow=%d", processes.pxgo, processes.pxgow)
	}

	occupied, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("occupy background failure port: %w", err)
	}
	bad := exec.Command(binaryPath, args...)
	bad.Env = append(os.Environ(), "PXGO_PROXY=DIRECT")
	badOut, badErr := bad.CombinedOutput()
	_ = occupied.Close()
	if badErr == nil {
		return errors.New("background launch unexpectedly succeeded on occupied port")
	}
	if len(strings.TrimSpace(string(badOut))) == 0 {
		return errors.New("background startup failure produced no console diagnostic")
	}
	for i := 0; i < 40; i++ {
		processes, err = windowsPxGoProcesses(binaryPath, backgroundPath)
		if err != nil {
			return err
		}
		if processes.pxgo == 0 && processes.pxgow == 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("failed background launch left processes: pxgo=%d pxgow=%d", processes.pxgo, processes.pxgow)
}

type windowsProcessCounts struct {
	pxgo  int
	pxgow int
}

func windowsPxGoProcesses(binaryPath, backgroundPath string) (windowsProcessCounts, error) {
	if runtime.GOOS != "windows" {
		return windowsProcessCounts{}, errors.New("Windows process inspection requires a Windows host")
	}
	dir := strings.ToLower(filepath.Clean(filepath.Dir(binaryPath)))
	ps := fmt.Sprintf(`$dir=%q; $items=Get-CimInstance Win32_Process | Where-Object { ($_.Name -ieq 'pxgo.exe' -or $_.Name -ieq 'pxgow.exe') -and ([IO.Path]::GetDirectoryName($_.ExecutablePath).ToLower() -eq $dir) }; $a=@($items | Where-Object {$_.Name -ieq 'pxgo.exe'}).Count; $b=@($items | Where-Object {$_.Name -ieq 'pxgow.exe'}).Count; Write-Output "$a $b"`, dir)
	out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", ps).CombinedOutput()
	if err != nil {
		return windowsProcessCounts{}, fmt.Errorf("inspect Windows process tree: %w: %s", err, out)
	}
	var counts windowsProcessCounts
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d %d", &counts.pxgo, &counts.pxgow); err != nil {
		return windowsProcessCounts{}, fmt.Errorf("parse Windows process counts %q: %w", out, err)
	}
	_ = backgroundPath
	return counts, nil
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func waitForPort(port int, timeout time.Duration) error {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 150*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("%s did not open", addr)
}
