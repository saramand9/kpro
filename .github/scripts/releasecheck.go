// Command releasecheck validates release tags and GoReleaser output.
package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

var fullSemverTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

type artifact struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	GoOS   string `json:"goos"`
	GoArch string `json:"goarch"`
	Type   string `json:"type"`
}

type target struct {
	goos   string
	goarch string
}

var expectedTargets = []target{
	{goos: "darwin", goarch: "amd64"},
	{goos: "darwin", goarch: "arm64"},
	{goos: "linux", goarch: "amd64"},
	{goos: "linux", goarch: "arm64"},
	{goos: "windows", goarch: "amd64"},
	{goos: "windows", goarch: "arm64"},
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "releasecheck:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: releasecheck <validate-tag|verify-artifacts>")
	}
	switch args[0] {
	case "validate-tag":
		flags := flag.NewFlagSet("validate-tag", flag.ContinueOnError)
		tag := flags.String("tag", "", "release tag")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("validate-tag accepts only --tag")
		}
		if err := validateTag(*tag); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "release tag %s is valid\n", *tag)
		return nil
	case "verify-artifacts":
		flags := flag.NewFlagSet("verify-artifacts", flag.ContinueOnError)
		dist := flags.String("dist", "dist", "GoReleaser output directory")
		tag := flags.String("tag", "", "release tag")
		commit := flags.String("commit", "", "release commit")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("verify-artifacts accepts only --dist, --tag, and --commit")
		}
		if err := verifyArtifacts(*dist, *tag, *commit); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "release artifacts for %s are complete and internally consistent\n", *tag)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func validateTag(tag string) error {
	if !fullSemverTag.MatchString(tag) || !semver.IsValid(tag) {
		return fmt.Errorf("tag %q is not a full v-prefixed semantic version", tag)
	}
	return nil
}

func verifyArtifacts(dist, tag, expectedCommit string) error {
	if err := validateTag(tag); err != nil {
		return err
	}
	if expectedCommit == "" {
		return errors.New("release commit is empty")
	}
	data, err := os.ReadFile(filepath.Join(dist, "artifacts.json")) // #nosec G304 -- Local CLI reads an operator-selected project or artifact path.
	if err != nil {
		return fmt.Errorf("read artifact catalog: %w", err)
	}
	var catalog []artifact
	if err := json.Unmarshal(data, &catalog); err != nil {
		return fmt.Errorf("decode artifact catalog: %w", err)
	}
	archives, sboms, checksum, err := validateCatalog(dist, catalog)
	if err != nil {
		return err
	}
	if err := verifyChecksums(dist, checksum, archives, sboms); err != nil {
		return err
	}
	native, err := selectNativeArchive(archives, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	return verifyNativeVersion(dist, native, tag, expectedCommit)
}

func selectNativeArchive(archives map[target]artifact, goos, goarch string) (artifact, error) {
	host := target{goos: goos, goarch: goarch}
	supported := false
	for _, expected := range expectedTargets {
		if expected == host {
			supported = true
			break
		}
	}
	if !supported {
		return artifact{}, fmt.Errorf("host target %s/%s is not in the release matrix", goos, goarch)
	}
	native, ok := archives[host]
	if !ok {
		return artifact{}, fmt.Errorf("archive for host target %s/%s is missing", goos, goarch)
	}
	return native, nil
}

func validateCatalog(dist string, catalog []artifact) (map[target]artifact, map[string]artifact, artifact, error) {
	archives := make(map[target]artifact, len(expectedTargets))
	sboms := make(map[string]artifact)
	var checksums []artifact
	for _, item := range catalog {
		switch item.Type {
		case "Archive":
			key := target{goos: item.GoOS, goarch: item.GoArch}
			if _, exists := archives[key]; exists {
				return nil, nil, artifact{}, fmt.Errorf("duplicate archive for %s/%s", item.GoOS, item.GoArch)
			}
			archives[key] = item
		case "SBOM":
			if _, exists := sboms[item.Name]; exists {
				return nil, nil, artifact{}, fmt.Errorf("duplicate SBOM %q", item.Name)
			}
			sboms[item.Name] = item
		case "Checksum":
			checksums = append(checksums, item)
		}
	}
	if len(archives) != len(expectedTargets) {
		return nil, nil, artifact{}, fmt.Errorf("got %d release archives, want %d", len(archives), len(expectedTargets))
	}
	for _, expected := range expectedTargets {
		item, ok := archives[expected]
		if !ok {
			return nil, nil, artifact{}, fmt.Errorf("archive for %s/%s is missing", expected.goos, expected.goarch)
		}
		if err := verifyRegularFile(dist, item); err != nil {
			return nil, nil, artifact{}, err
		}
		if err := verifyArchive(dist, item, expected.goos); err != nil {
			return nil, nil, artifact{}, err
		}
		expectedSBOM := item.Name + ".sbom.json"
		sbom, ok := sboms[expectedSBOM]
		if !ok {
			return nil, nil, artifact{}, fmt.Errorf("SBOM %q is missing", expectedSBOM)
		}
		if err := verifyRegularFile(dist, sbom); err != nil {
			return nil, nil, artifact{}, err
		}
		if err := verifySBOM(dist, sbom); err != nil {
			return nil, nil, artifact{}, err
		}
	}
	if len(sboms) != len(expectedTargets) {
		return nil, nil, artifact{}, fmt.Errorf("got %d SBOMs, want %d", len(sboms), len(expectedTargets))
	}
	if len(checksums) != 1 || checksums[0].Name != "checksums.txt" {
		names := make([]string, 0, len(checksums))
		for _, item := range checksums {
			names = append(names, item.Name)
		}
		sort.Strings(names)
		return nil, nil, artifact{}, fmt.Errorf("checksum artifacts are %v, want [checksums.txt]", names)
	}
	if err := verifyRegularFile(dist, checksums[0]); err != nil {
		return nil, nil, artifact{}, err
	}
	return archives, sboms, checksums[0], nil
}

func verifyRegularFile(dist string, item artifact) error {
	path, err := resolveArtifactPath(dist, item)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s artifact %q: %w", item.Type, item.Name, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("%s artifact %q is not a non-empty regular file", item.Type, item.Name)
	}
	return nil
}

func resolveArtifactPath(dist string, item artifact) (string, error) {
	if err := validateArtifactName(item.Name); err != nil {
		return "", fmt.Errorf("%s artifact: %w", item.Type, err)
	}
	root, err := filepath.Abs(dist)
	if err != nil {
		return "", fmt.Errorf("resolve dist directory: %w", err)
	}
	var candidates []string
	if item.Path != "" {
		path := filepath.FromSlash(item.Path)
		if filepath.IsAbs(path) {
			candidates = append(candidates, filepath.Clean(path))
		} else {
			absolute, err := filepath.Abs(path)
			if err != nil {
				return "", fmt.Errorf("resolve artifact path %q: %w", item.Path, err)
			}
			candidates = append(candidates, absolute)
		}
	}
	candidates = append(candidates, filepath.Join(root, filepath.Base(item.Name)))
	for _, candidate := range candidates {
		relative, err := filepath.Rel(root, candidate)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s artifact %q is outside %s or does not exist", item.Type, item.Name, root)
}

func validateArtifactName(name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || filepath.Base(name) != name {
		return fmt.Errorf("artifact name %q is not a safe base name", name)
	}
	return nil
}

func verifyChecksums(
	dist string,
	checksum artifact,
	archives map[target]artifact,
	sboms map[string]artifact,
) error {
	checksumPath, err := resolveArtifactPath(dist, checksum)
	if err != nil {
		return err
	}
	file, err := os.Open(checksumPath) // #nosec G304 -- Local CLI reads an operator-selected project or artifact path.
	if err != nil {
		return fmt.Errorf("open checksums: %w", err)
	}
	defer file.Close()

	expected := make(map[string]artifact, len(archives)+len(sboms))
	for _, item := range archives {
		if _, exists := expected[item.Name]; exists {
			return fmt.Errorf("duplicate expected checksum entry %q", item.Name)
		}
		expected[item.Name] = item
	}
	for _, item := range sboms {
		if _, exists := expected[item.Name]; exists {
			return fmt.Errorf("duplicate expected checksum entry %q", item.Name)
		}
		expected[item.Name] = item
	}
	if len(expected) != len(expectedTargets)*2 {
		return fmt.Errorf("got %d checksum targets, want %d", len(expected), len(expectedTargets)*2)
	}

	recorded := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			return fmt.Errorf("invalid checksum line %q", scanner.Text())
		}
		if _, err := hex.DecodeString(fields[0]); err != nil || len(fields[0]) != sha256.Size*2 {
			return fmt.Errorf("invalid SHA-256 digest for %q", fields[1])
		}
		name := strings.TrimPrefix(fields[1], "*")
		if err := validateArtifactName(name); err != nil {
			return fmt.Errorf("invalid checksum filename: %w", err)
		}
		if _, exists := recorded[name]; exists {
			return fmt.Errorf("duplicate checksum entry %q", name)
		}
		if _, ok := expected[name]; !ok {
			return fmt.Errorf("checksum contains unknown artifact %q", name)
		}
		recorded[name] = strings.ToLower(fields[0])
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read checksums: %w", err)
	}
	if len(recorded) != len(expected) {
		missing := make([]string, 0, len(expected)-len(recorded))
		for name := range expected {
			if _, ok := recorded[name]; !ok {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		return fmt.Errorf("checksums cover %d artifacts, want %d; missing %v", len(recorded), len(expected), missing)
	}
	for name, item := range expected {
		path, err := resolveArtifactPath(dist, item)
		if err != nil {
			return err
		}
		recordedDigest := recorded[name]
		actual, err := fileSHA256(path)
		if err != nil {
			return err
		}
		if actual != recordedDigest {
			return fmt.Errorf("checksum mismatch for artifact %q", name)
		}
	}
	return nil
}

func verifySBOM(dist string, item artifact) error {
	sbomPath, err := resolveArtifactPath(dist, item)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(sbomPath) // #nosec G304 -- Local CLI reads an operator-selected project or artifact path.
	if err != nil {
		return fmt.Errorf("read SBOM %q: %w", item.Name, err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("decode SBOM %q as SPDX JSON: %w", item.Name, err)
	}
	var spdxVersion string
	if raw, ok := document["spdxVersion"]; !ok {
		return fmt.Errorf("SBOM %q has no spdxVersion", item.Name)
	} else if err := json.Unmarshal(raw, &spdxVersion); err != nil {
		return fmt.Errorf("SBOM %q has a non-string spdxVersion", item.Name)
	}
	if !strings.HasPrefix(spdxVersion, "SPDX-") {
		return fmt.Errorf("SBOM %q has invalid spdxVersion %q", item.Name, spdxVersion)
	}
	rawPackages, ok := document["packages"]
	if !ok {
		return fmt.Errorf("SBOM %q has no packages array", item.Name)
	}
	trimmedPackages := bytes.TrimSpace(rawPackages)
	if len(trimmedPackages) == 0 || trimmedPackages[0] != '[' {
		return fmt.Errorf("SBOM %q packages field is not an array", item.Name)
	}
	var packages []json.RawMessage
	if err := json.Unmarshal(trimmedPackages, &packages); err != nil {
		return fmt.Errorf("SBOM %q packages field is not an array: %w", item.Name, err)
	}
	return nil
}

func verifyArchive(dist string, item artifact, goos string) error {
	archivePath, err := resolveArtifactPath(dist, item)
	if err != nil {
		return err
	}
	switch goos {
	case "windows":
		if !strings.HasSuffix(item.Name, ".zip") {
			return fmt.Errorf("archive %q for windows must use .zip format", item.Name)
		}
		return verifyZipArchive(archivePath, item.Name, requiredArchiveEntries(goos))
	default:
		if !strings.HasSuffix(item.Name, ".tar.gz") {
			return fmt.Errorf("archive %q for %s must use .tar.gz format", item.Name, goos)
		}
		return verifyTarGzArchive(archivePath, item.Name, requiredArchiveEntries(goos))
	}
}

func requiredArchiveEntries(goos string) map[string]struct{} {
	binary := "kpro"
	if goos == "windows" {
		binary = "kpro.exe"
	}
	return map[string]struct{}{
		binary:         {},
		"LICENSE":      {},
		"README.md":    {},
		"CHANGELOG.md": {},
	}
}

func validateArchiveEntry(
	archiveName string,
	entryName string,
	regular bool,
	size int64,
	required map[string]struct{},
	seen map[string]struct{},
) error {
	cleaned := path.Clean(entryName)
	if entryName == "" || strings.Contains(entryName, `\`) || path.IsAbs(entryName) ||
		cleaned != entryName || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("archive %q contains unsafe entry %q", archiveName, entryName)
	}
	if strings.Contains(entryName, "/") {
		return fmt.Errorf("archive %q contains non-root entry %q", archiveName, entryName)
	}
	if _, ok := required[entryName]; !ok {
		return fmt.Errorf("archive %q contains unexpected entry %q", archiveName, entryName)
	}
	if _, duplicate := seen[entryName]; duplicate {
		return fmt.Errorf("archive %q contains duplicate entry %q", archiveName, entryName)
	}
	if !regular {
		return fmt.Errorf("archive %q entry %q is not a regular file", archiveName, entryName)
	}
	if size <= 0 {
		return fmt.Errorf("archive %q entry %q is empty", archiveName, entryName)
	}
	if size > maxArchiveEntryBytes {
		return fmt.Errorf("archive %q entry %q exceeds size limit", archiveName, entryName)
	}
	seen[entryName] = struct{}{}
	return nil
}

func verifyArchiveCoverage(archiveName string, required, seen map[string]struct{}) error {
	if len(seen) == len(required) {
		return nil
	}
	missing := make([]string, 0, len(required)-len(seen))
	for name := range required {
		if _, ok := seen[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return fmt.Errorf("archive %q is missing entries %v", archiveName, missing)
}

// Release archives contain a CLI binary and small documentation files. Bound
// both individual entries and the whole decompressed stream, including padding.
const maxArchiveEntryBytes int64 = 512 << 20
const maxArchiveStreamBytes int64 = 2 << 30

func copyArchiveEntry(dst io.Writer, src io.Reader) (int64, error) {
	return copyArchiveBytes(dst, src, maxArchiveEntryBytes)
}

func copyArchiveBytes(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	written, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return written, err
	}
	if written > limit {
		return written, fmt.Errorf("archive entry exceeds %d bytes", limit)
	}
	return written, nil
}

func verifyTarGzArchive(archivePath, archiveName string, required map[string]struct{}) error {
	file, err := os.Open(archivePath) // #nosec G304 -- Local CLI reads an operator-selected project or artifact path.
	if err != nil {
		return fmt.Errorf("open archive %q: %w", archiveName, err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open archive %q gzip stream: %w", archiveName, err)
	}
	defer gzipReader.Close()

	seen := make(map[string]struct{}, len(required))
	limited := &io.LimitedReader{R: gzipReader, N: maxArchiveStreamBytes + 1}
	tarReader := tar.NewReader(limited)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive %q: %w", archiveName, err)
		}
		regular := header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA
		if err := validateArchiveEntry(archiveName, header.Name, regular, header.Size, required, seen); err != nil {
			return err
		}
		written, err := copyArchiveEntry(io.Discard, tarReader)
		if err != nil {
			return fmt.Errorf("read archive %q entry %q: %w", archiveName, header.Name, err)
		}
		if written != header.Size {
			return fmt.Errorf("archive %q entry %q has size %d, read %d", archiveName, header.Name, header.Size, written)
		}
	}
	if _, err := io.Copy(io.Discard, limited); err != nil {
		return fmt.Errorf("finish reading archive %q gzip stream: %w", archiveName, err)
	}
	if limited.N == 0 {
		return fmt.Errorf("archive %q exceeds decompressed stream limit", archiveName)
	}
	return verifyArchiveCoverage(archiveName, required, seen)
}

func verifyZipArchive(archivePath, archiveName string, required map[string]struct{}) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open archive %q as zip: %w", archiveName, err)
	}
	defer reader.Close()

	seen := make(map[string]struct{}, len(required))
	for _, entry := range reader.File {
		if entry.UncompressedSize64 > uint64(maxArchiveEntryBytes) {
			return fmt.Errorf("archive %q entry %q exceeds size limit", archiveName, entry.Name)
		}
		if err := validateArchiveEntry(
			archiveName,
			entry.Name,
			entry.FileInfo().Mode().IsRegular(),
			int64(entry.UncompressedSize64), // #nosec G115 -- The entry size is bounded to 512 MiB above before conversion.
			required,
			seen,
		); err != nil {
			return err
		}
		file, err := entry.Open()
		if err != nil {
			return fmt.Errorf("open archive %q entry %q: %w", archiveName, entry.Name, err)
		}
		written, copyErr := copyArchiveEntry(io.Discard, file)
		closeErr := file.Close()
		if copyErr != nil {
			return fmt.Errorf("read archive %q entry %q: %w", archiveName, entry.Name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close archive %q entry %q: %w", archiveName, entry.Name, closeErr)
		}
		// #nosec G115 -- The entry size is bounded to 512 MiB above before conversion.
		if written != int64(entry.UncompressedSize64) {
			return fmt.Errorf(
				"archive %q entry %q has size %d, read %d",
				archiveName,
				entry.Name,
				entry.UncompressedSize64,
				written,
			)
		}
	}
	return verifyArchiveCoverage(archiveName, required, seen)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- Local CLI reads an operator-selected project or artifact path.
	if err != nil {
		return "", fmt.Errorf("open %q for hashing: %w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash %q: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func verifyNativeVersion(dist string, archive artifact, tag, expectedCommit string) error {
	if archive.GoOS != runtime.GOOS || archive.GoArch != runtime.GOARCH {
		return fmt.Errorf(
			"cannot execute %s/%s release binary on host %s/%s",
			archive.GoOS,
			archive.GoArch,
			runtime.GOOS,
			runtime.GOARCH,
		)
	}
	archivePath, err := resolveArtifactPath(dist, archive)
	if err != nil {
		return err
	}
	tempDir, err := os.MkdirTemp("", "kpro-releasecheck-*")
	if err != nil {
		return fmt.Errorf("create verification directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	binaryName, err := nativeBinaryName(archive.GoOS)
	if err != nil {
		return err
	}
	binaryPath := filepath.Join(tempDir, binaryName)
	if err := extractNativeBinary(
		archivePath,
		target{goos: archive.GoOS, goarch: archive.GoArch},
		binaryPath,
	); err != nil {
		return err
	}
	if archive.GoOS != "windows" {
		// #nosec G302 -- Owner-only executable in a private temporary directory.
		if err := os.Chmod(binaryPath, 0o700); err != nil {
			return fmt.Errorf("make release binary executable: %w", err)
		}
	}
	output, err := exec.Command(binaryPath, "version").CombinedOutput() // #nosec G204 -- Deliberately executes the locally built release artifact from a private temporary directory.
	if err != nil {
		return fmt.Errorf("run release binary version command: %w: %s", err, output)
	}
	return validateVersionOutput(string(output), tag, expectedCommit)
}

func nativeBinaryName(goos string) (string, error) {
	switch goos {
	case "windows":
		return "kpro.exe", nil
	case "linux", "darwin":
		return "kpro", nil
	default:
		return "", fmt.Errorf("unsupported native release target %q", goos)
	}
}

func extractNativeBinary(archivePath string, native target, targetPath string) error {
	binaryName, err := nativeBinaryName(native.goos)
	if err != nil {
		return err
	}
	if filepath.Base(targetPath) != binaryName {
		return fmt.Errorf("native extraction target must end in %q", binaryName)
	}
	if native.goos == "windows" {
		return extractZipFile(archivePath, binaryName, targetPath)
	}
	return extractTarGzFile(archivePath, binaryName, targetPath)
}

func extractTarGzFile(archivePath, wantedBase, targetPath string) error {
	file, err := os.Open(archivePath) // #nosec G304 -- Local CLI reads an operator-selected project or artifact path.
	if err != nil {
		return fmt.Errorf("open native archive: %w", err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open native archive gzip stream: %w", err)
	}
	defer gzipReader.Close()
	limited := &io.LimitedReader{R: gzipReader, N: maxArchiveStreamBytes + 1}
	tarReader := tar.NewReader(limited)
	found := false
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read native archive: %w", err)
		}
		if header.Size < 0 || header.Size > maxArchiveEntryBytes {
			return fmt.Errorf("native archive entry %q exceeds size limit", header.Name)
		}
		if header.Name != wantedBase {
			continue
		}
		if found {
			return fmt.Errorf("native archive %q contains duplicate %q", archivePath, wantedBase)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("native archive %q entry %q is not a regular file", archivePath, wantedBase)
		}
		target, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- Fixed binary basename is extracted exclusively into a private temporary directory.
		if err != nil {
			return fmt.Errorf("create extracted release binary: %w", err)
		}
		_, copyErr := copyArchiveEntry(target, tarReader)
		closeErr := target.Close()
		if copyErr != nil {
			return fmt.Errorf("extract release binary: %w", copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close extracted release binary: %w", closeErr)
		}
		found = true
	}
	if limited.N == 0 {
		return fmt.Errorf("native archive exceeds decompressed stream limit")
	}
	if found {
		return nil
	}
	return fmt.Errorf("native archive %q does not contain %q", archivePath, wantedBase)
}

func extractZipFile(archivePath, wantedBase, targetPath string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open native zip archive: %w", err)
	}
	defer reader.Close()

	var wanted *zip.File
	for _, entry := range reader.File {
		if entry.Name != wantedBase {
			continue
		}
		if wanted != nil {
			return fmt.Errorf("native archive %q contains duplicate %q", archivePath, wantedBase)
		}
		if !entry.FileInfo().Mode().IsRegular() {
			return fmt.Errorf("native archive %q entry %q is not a regular file", archivePath, wantedBase)
		}
		wanted = entry
	}
	if wanted == nil {
		return fmt.Errorf("native archive %q does not contain %q", archivePath, wantedBase)
	}
	if wanted.UncompressedSize64 > uint64(maxArchiveEntryBytes) {
		return fmt.Errorf("native archive entry %q exceeds size limit", wantedBase)
	}

	source, err := wanted.Open()
	if err != nil {
		return fmt.Errorf("open native archive entry %q: %w", wantedBase, err)
	}
	target, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- Fixed binary basename is extracted exclusively into a private temporary directory.
	if err != nil {
		_ = source.Close() // Preserve the target creation error.
		return fmt.Errorf("create extracted release binary: %w", err)
	}
	_, copyErr := copyArchiveEntry(target, source)
	sourceCloseErr := source.Close()
	targetCloseErr := target.Close()
	if copyErr != nil {
		return fmt.Errorf("extract release binary: %w", copyErr)
	}
	if sourceCloseErr != nil {
		return fmt.Errorf("close native archive entry: %w", sourceCloseErr)
	}
	if targetCloseErr != nil {
		return fmt.Errorf("close extracted release binary: %w", targetCloseErr)
	}
	return nil
}

func validateVersionOutput(output, tag, expectedCommit string) error {
	output = strings.TrimSpace(output)
	prefix := "kpro " + tag + " (commit "
	if !strings.HasPrefix(output, prefix) {
		return fmt.Errorf("release binary reports unexpected version: %q", output)
	}
	remainder := strings.TrimPrefix(output, prefix)
	separator := strings.Index(remainder, ", built ")
	if separator < 0 {
		return fmt.Errorf("release binary reports malformed build metadata: %q", output)
	}
	actualCommit := remainder[:separator]
	if len(actualCommit) < 7 || !(strings.HasPrefix(expectedCommit, actualCommit) || strings.HasPrefix(actualCommit, expectedCommit)) {
		return fmt.Errorf("release binary reports commit %q, want %q", actualCommit, expectedCommit)
	}
	buildMetadata := remainder[separator+len(", built "):]
	if strings.HasPrefix(buildMetadata, "unknown,") || strings.HasPrefix(buildMetadata, ",") {
		return fmt.Errorf("release binary has no build date: %q", output)
	}
	return nil
}
