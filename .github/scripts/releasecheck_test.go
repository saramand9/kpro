package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidateTag(t *testing.T) {
	for _, tag := range []string{
		"v0.1.0",
		"v1.2.3-rc.1",
		"v1.2.3+build.5",
	} {
		if err := validateTag(tag); err != nil {
			t.Errorf("validateTag(%q): %v", tag, err)
		}
	}
	for _, tag := range []string{
		"",
		"0.1.0",
		"v1",
		"v1.2",
		"v01.2.3",
		"v1.2.3-01",
		"vnext",
	} {
		if err := validateTag(tag); err == nil {
			t.Errorf("validateTag(%q) unexpectedly succeeded", tag)
		}
	}
}

func TestArchiveReadLimit(t *testing.T) {
	for _, size := range []int{3, 4, 5, 64} {
		var output strings.Builder
		written, err := copyArchiveBytes(&output, strings.NewReader(strings.Repeat("a", size)), 4)
		if (err != nil) != (size > 4) {
			t.Fatalf("size %d: error %v", size, err)
		}
		if written > 5 || output.Len() > 5 {
			t.Fatalf("read beyond limit sentinel: %d", written)
		}
	}
}

func TestArchiveRejectsOversizedEntry(t *testing.T) {
	required := map[string]struct{}{"kpro": {}}
	seen := map[string]struct{}{}
	if err := validateArchiveEntry("test.tar.gz", "kpro", true, maxArchiveEntryBytes+1, required, seen); err == nil {
		t.Fatal("oversized entry accepted")
	}
	archivePath := filepath.Join(t.TempDir(), "oversized.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "kpro", Mode: 0o700, Size: maxArchiveEntryBytes + 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	// Deliberately truncated oversized header must be rejected before extraction.
	_ = tarWriter.Close()
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "kpro")
	if err := extractTarGzFile(archivePath, "kpro", target); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized extraction error = %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("extraction created output: %v", err)
	}
}

func TestValidateVersionOutput(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	valid := "kpro v0.1.0 (commit 0123456, built 2026-07-24T12:00:00Z, linux/amd64, go1.26.5)"
	if err := validateVersionOutput(valid, "v0.1.0", commit); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{
		"kpro devel (commit 0123456, built 2026-07-24T12:00:00Z, linux/amd64, go1.26.5)",
		"kpro v0.1.0 (commit fedcba9, built 2026-07-24T12:00:00Z, linux/amd64, go1.26.5)",
		"kpro v0.1.0 (commit 0123456, built unknown, linux/amd64, go1.26.5)",
	} {
		if err := validateVersionOutput(output, "v0.1.0", commit); err == nil {
			t.Errorf("validateVersionOutput(%q) unexpectedly succeeded", output)
		}
	}
}

func TestCatalogAndChecksums(t *testing.T) {
	dist := t.TempDir()
	var catalog []artifact
	var checksumLines strings.Builder
	for _, expected := range expectedTargets {
		extension := ".tar.gz"
		if expected.goos == "windows" {
			extension = ".zip"
		}
		name := fmt.Sprintf("kpro_0.1.0_%s_%s%s", expected.goos, expected.goarch, extension)
		writeReleaseArchive(t, filepath.Join(dist, name), expected.goos, validArchiveEntries(expected.goos))
		appendChecksumLine(t, &checksumLines, filepath.Join(dist, name), name)
		catalog = append(catalog, artifact{
			Name: name, GoOS: expected.goos, GoArch: expected.goarch, Type: "Archive",
		})
		sbomName := name + ".sbom.json"
		if err := os.WriteFile(
			filepath.Join(dist, sbomName),
			[]byte(`{"spdxVersion":"SPDX-2.3","packages":[{"name":"kpro"}]}`),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		appendChecksumLine(t, &checksumLines, filepath.Join(dist, sbomName), sbomName)
		catalog = append(catalog, artifact{Name: sbomName, Type: "SBOM"})
	}
	if err := os.WriteFile(filepath.Join(dist, "checksums.txt"), []byte(checksumLines.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog = append(catalog, artifact{Name: "checksums.txt", Type: "Checksum"})

	archives, sboms, checksum, err := validateCatalog(dist, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksums(dist, checksum, archives, sboms); err != nil {
		t.Fatal(err)
	}
	corrupt := sboms[archives[target{goos: "linux", goarch: "amd64"}].Name+".sbom.json"]
	if err := os.WriteFile(filepath.Join(dist, corrupt.Name), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksums(dist, checksum, archives, sboms); err == nil {
		t.Fatal("corrupt SBOM unexpectedly passed checksum verification")
	}
}

func TestVerifyChecksumsRequiresExactCoverage(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testing.T, string, *[]string, []string)
		wantErr string
	}{
		{
			name: "duplicate",
			mutate: func(_ *testing.T, _ string, lines *[]string, _ []string) {
				*lines = append(*lines, (*lines)[0])
			},
			wantErr: "duplicate checksum entry",
		},
		{
			name: "missing",
			mutate: func(_ *testing.T, _ string, lines *[]string, _ []string) {
				*lines = (*lines)[:len(*lines)-1]
			},
			wantErr: "missing",
		},
		{
			name: "unknown",
			mutate: func(_ *testing.T, _ string, lines *[]string, _ []string) {
				*lines = append(*lines, strings.Repeat("0", sha256.Size*2)+"  unknown.txt")
			},
			wantErr: "unknown artifact",
		},
		{
			name: "unsafe filename",
			mutate: func(_ *testing.T, _ string, lines *[]string, _ []string) {
				*lines = append(*lines, strings.Repeat("0", sha256.Size*2)+"  ../archive")
			},
			wantErr: "safe base name",
		},
		{
			name: "archive digest mismatch",
			mutate: func(t *testing.T, dist string, _ *[]string, names []string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dist, names[0]), []byte("changed"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "checksum mismatch",
		},
		{
			name: "SBOM digest mismatch",
			mutate: func(t *testing.T, dist string, _ *[]string, names []string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dist, names[1]), []byte("changed"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "checksum mismatch",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dist, archives, sboms, checksum, lines, names := checksumFixture(t)
			test.mutate(t, dist, &lines, names)
			if err := os.WriteFile(
				filepath.Join(dist, checksum.Name),
				[]byte(strings.Join(lines, "\n")+"\n"),
				0o644,
			); err != nil {
				t.Fatal(err)
			}
			err := verifyChecksums(dist, checksum, archives, sboms)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("verifyChecksums() error = %v, want error containing %q", err, test.wantErr)
			}
		})
	}
}

func TestVerifySBOM(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		wantErr  string
	}{
		{name: "Syft SPDX JSON", contents: `{"spdxVersion":"SPDX-2.3","packages":[],"relationships":[]}`},
		{name: "invalid JSON", contents: `{`, wantErr: "decode SBOM"},
		{name: "non-object", contents: `[]`, wantErr: "decode SBOM"},
		{name: "missing version", contents: `{"packages":[]}`, wantErr: "spdxVersion"},
		{name: "non-string version", contents: `{"spdxVersion":2.3,"packages":[]}`, wantErr: "non-string"},
		{name: "invalid version", contents: `{"spdxVersion":"2.3","packages":[]}`, wantErr: "invalid spdxVersion"},
		{name: "missing packages", contents: `{"spdxVersion":"SPDX-2.3"}`, wantErr: "no packages array"},
		{name: "null packages", contents: `{"spdxVersion":"SPDX-2.3","packages":null}`, wantErr: "not an array"},
		{name: "object packages", contents: `{"spdxVersion":"SPDX-2.3","packages":{}}`, wantErr: "not an array"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dist := t.TempDir()
			item := artifact{Name: "archive.sbom.json", Type: "SBOM"}
			if err := os.WriteFile(filepath.Join(dist, item.Name), []byte(test.contents), 0o644); err != nil {
				t.Fatal(err)
			}
			err := verifySBOM(dist, item)
			if test.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("verifySBOM() error = %v, want error containing %q", err, test.wantErr)
			}
		})
	}
}

func TestVerifyArchive(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run("valid "+goos, func(t *testing.T) {
			dist := t.TempDir()
			extension := ".tar.gz"
			if goos == "windows" {
				extension = ".zip"
			}
			item := artifact{Name: "kpro" + extension, Type: "Archive"}
			writeReleaseArchive(t, filepath.Join(dist, item.Name), goos, validArchiveEntries(goos))
			if err := verifyArchive(dist, item, goos); err != nil {
				t.Fatal(err)
			}
		})
	}

	tests := []struct {
		name    string
		goos    string
		entries func(string) []testArchiveEntry
		wantErr string
	}{
		{
			name: "missing entry", goos: "linux",
			entries: func(goos string) []testArchiveEntry {
				return removeArchiveEntry(validArchiveEntries(goos), "README.md")
			},
			wantErr: "missing entries",
		},
		{
			name: "unexpected entry", goos: "linux",
			entries: func(goos string) []testArchiveEntry {
				return append(validArchiveEntries(goos), testArchiveEntry{name: "NOTES.md", contents: "notes"})
			},
			wantErr: "unexpected entry",
		},
		{
			name: "path traversal tar", goos: "linux",
			entries: func(goos string) []testArchiveEntry {
				return append(validArchiveEntries(goos), testArchiveEntry{name: "../kpro", contents: "escape"})
			},
			wantErr: "unsafe entry",
		},
		{
			name: "path traversal zip", goos: "windows",
			entries: func(goos string) []testArchiveEntry {
				return append(validArchiveEntries(goos), testArchiveEntry{name: "../kpro.exe", contents: "escape"})
			},
			wantErr: "unsafe entry",
		},
		{
			name: "duplicate tar entry", goos: "darwin",
			entries: func(goos string) []testArchiveEntry {
				return append(validArchiveEntries(goos), testArchiveEntry{name: "LICENSE", contents: "again"})
			},
			wantErr: "duplicate entry",
		},
		{
			name: "duplicate zip entry", goos: "windows",
			entries: func(goos string) []testArchiveEntry {
				return append(validArchiveEntries(goos), testArchiveEntry{name: "LICENSE", contents: "again"})
			},
			wantErr: "duplicate entry",
		},
		{
			name: "wrong binary", goos: "linux",
			entries: func(goos string) []testArchiveEntry {
				entries := removeArchiveEntry(validArchiveEntries(goos), "kpro")
				return append(entries, testArchiveEntry{name: "kpro.exe", contents: "binary"})
			},
			wantErr: "unexpected entry",
		},
		{
			name: "symlink", goos: "linux",
			entries: func(goos string) []testArchiveEntry {
				entries := validArchiveEntries(goos)
				for index := range entries {
					if entries[index].name == "LICENSE" {
						entries[index].contents = ""
						entries[index].typeflag = tar.TypeSymlink
					}
				}
				return entries
			},
			wantErr: "not a regular file",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dist := t.TempDir()
			extension := ".tar.gz"
			if test.goos == "windows" {
				extension = ".zip"
			}
			item := artifact{Name: "kpro" + extension, Type: "Archive"}
			writeReleaseArchive(t, filepath.Join(dist, item.Name), test.goos, test.entries(test.goos))
			err := verifyArchive(dist, item, test.goos)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("verifyArchive() error = %v, want error containing %q", err, test.wantErr)
			}
		})
	}

	t.Run("wrong extension", func(t *testing.T) {
		dist := t.TempDir()
		item := artifact{Name: "kpro.zip", Type: "Archive"}
		writeReleaseArchive(t, filepath.Join(dist, item.Name), "windows", validArchiveEntries("windows"))
		err := verifyArchive(dist, item, "linux")
		if err == nil || !strings.Contains(err.Error(), "must use .tar.gz") {
			t.Fatalf("verifyArchive() error = %v, want format error", err)
		}
	})
}

func TestSelectNativeArchive(t *testing.T) {
	windows := artifact{
		Name: "kpro_0.1.0_windows_amd64.zip", GoOS: "windows", GoArch: "amd64", Type: "Archive",
	}
	archives := map[target]artifact{
		{goos: "windows", goarch: "amd64"}: windows,
	}
	got, err := selectNativeArchive(archives, "windows", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if got != windows {
		t.Fatalf("selectNativeArchive() = %#v, want %#v", got, windows)
	}
	if _, err := selectNativeArchive(archives, "linux", "amd64"); err == nil ||
		!strings.Contains(err.Error(), "archive for host target linux/amd64 is missing") {
		t.Fatalf("missing native archive error = %v", err)
	}
	if _, err := selectNativeArchive(archives, "freebsd", "amd64"); err == nil ||
		!strings.Contains(err.Error(), "not in the release matrix") {
		t.Fatalf("unsupported host target error = %v", err)
	}
}

func TestVerifyNativeVersionRejectsCrossPlatformArchive(t *testing.T) {
	otherGOOS := "linux"
	if runtime.GOOS == otherGOOS {
		otherGOOS = "windows"
	}
	err := verifyNativeVersion(
		t.TempDir(),
		artifact{Name: "unused", GoOS: otherGOOS, GoArch: runtime.GOARCH, Type: "Archive"},
		"v0.1.0",
		"0123456789abcdef0123456789abcdef01234567",
	)
	if err == nil || !strings.Contains(err.Error(), "cannot execute") {
		t.Fatalf("verifyNativeVersion() error = %v, want cross-platform rejection", err)
	}
}

func TestExtractNativeBinaryFromWindowsZip(t *testing.T) {
	dist := t.TempDir()
	archivePath := filepath.Join(dist, "kpro_windows_amd64.zip")
	writeZip(t, archivePath, []testArchiveEntry{{name: "kpro.exe", contents: "windows-binary"}})
	targetPath := filepath.Join(dist, "kpro.exe")
	if err := extractNativeBinary(
		archivePath,
		target{goos: "windows", goarch: "amd64"},
		targetPath,
	); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "windows-binary" {
		t.Fatalf("extracted contents = %q", contents)
	}
}

func TestExtractNativeBinaryFromTarGz(t *testing.T) {
	dist := t.TempDir()
	archivePath := filepath.Join(dist, "kpro_linux_amd64.tar.gz")
	writeTarGz(t, archivePath, []testArchiveEntry{{name: "kpro", contents: "linux-binary"}})
	targetPath := filepath.Join(dist, "kpro")
	if err := extractNativeBinary(
		archivePath,
		target{goos: "linux", goarch: "amd64"},
		targetPath,
	); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "linux-binary" {
		t.Fatalf("extracted contents = %q", contents)
	}
}

func TestNativeExtractionRejectsUnsafeOrAmbiguousEntries(t *testing.T) {
	t.Run("zip traversal", func(t *testing.T) {
		dist := t.TempDir()
		archivePath := filepath.Join(dist, "kpro.zip")
		writeZip(t, archivePath, []testArchiveEntry{{name: "../kpro.exe", contents: "escape"}})
		targetPath := filepath.Join(dist, "kpro.exe")
		err := extractNativeBinary(
			archivePath,
			target{goos: "windows", goarch: "amd64"},
			targetPath,
		)
		if err == nil || !strings.Contains(err.Error(), "does not contain") {
			t.Fatalf("extractNativeBinary() error = %v, want root-entry rejection", err)
		}
		if _, statErr := os.Stat(targetPath); !os.IsNotExist(statErr) {
			t.Fatalf("unsafe zip created extraction target: %v", statErr)
		}
	})

	t.Run("duplicate zip binary", func(t *testing.T) {
		dist := t.TempDir()
		archivePath := filepath.Join(dist, "kpro.zip")
		writeZip(t, archivePath, []testArchiveEntry{
			{name: "kpro.exe", contents: "first"},
			{name: "kpro.exe", contents: "second"},
		})
		err := extractNativeBinary(
			archivePath,
			target{goos: "windows", goarch: "amd64"},
			filepath.Join(dist, "kpro.exe"),
		)
		if err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("extractNativeBinary() error = %v, want duplicate rejection", err)
		}
	})

	t.Run("tar traversal", func(t *testing.T) {
		dist := t.TempDir()
		archivePath := filepath.Join(dist, "kpro.tar.gz")
		writeTarGz(t, archivePath, []testArchiveEntry{{name: "nested/kpro", contents: "escape"}})
		targetPath := filepath.Join(dist, "kpro")
		err := extractNativeBinary(
			archivePath,
			target{goos: "linux", goarch: "amd64"},
			targetPath,
		)
		if err == nil || !strings.Contains(err.Error(), "does not contain") {
			t.Fatalf("extractNativeBinary() error = %v, want root-entry rejection", err)
		}
		if _, statErr := os.Stat(targetPath); !os.IsNotExist(statErr) {
			t.Fatalf("unsafe tar created extraction target: %v", statErr)
		}
	})

	t.Run("wrong fixed target name", func(t *testing.T) {
		dist := t.TempDir()
		archivePath := filepath.Join(dist, "kpro.zip")
		writeZip(t, archivePath, []testArchiveEntry{{name: "kpro.exe", contents: "binary"}})
		err := extractNativeBinary(
			archivePath,
			target{goos: "windows", goarch: "amd64"},
			filepath.Join(dist, "renamed.exe"),
		)
		if err == nil || !strings.Contains(err.Error(), `must end in "kpro.exe"`) {
			t.Fatalf("extractNativeBinary() error = %v, want fixed-name rejection", err)
		}
	})
}

type testArchiveEntry struct {
	name     string
	contents string
	typeflag byte
}

func validArchiveEntries(goos string) []testArchiveEntry {
	binary := "kpro"
	if goos == "windows" {
		binary = "kpro.exe"
	}
	return []testArchiveEntry{
		{name: binary, contents: "binary"},
		{name: "LICENSE", contents: "license"},
		{name: "README.md", contents: "readme"},
		{name: "CHANGELOG.md", contents: "changelog"},
	}
}

func removeArchiveEntry(entries []testArchiveEntry, name string) []testArchiveEntry {
	result := make([]testArchiveEntry, 0, len(entries)-1)
	for _, entry := range entries {
		if entry.name != name {
			result = append(result, entry)
		}
	}
	return result
}

func writeReleaseArchive(t *testing.T, archivePath, goos string, entries []testArchiveEntry) {
	t.Helper()
	if goos == "windows" {
		writeZip(t, archivePath, entries)
		return
	}
	writeTarGz(t, archivePath, entries)
}

func writeTarGz(t *testing.T, archivePath string, entries []testArchiveEntry) {
	t.Helper()
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		size := int64(len(entry.contents))
		header := &tar.Header{Name: entry.name, Mode: 0o644, Size: size, Typeflag: typeflag}
		if typeflag == tar.TypeSymlink {
			header.Linkname = "LICENSE.target"
			header.Size = 0
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := io.WriteString(tarWriter, entry.contents); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeZip(t *testing.T, archivePath string, entries []testArchiveEntry) {
	t.Helper()
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	zipWriter := zip.NewWriter(file)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(0o644)
		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, entry.contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func appendChecksumLine(t *testing.T, builder *strings.Builder, filePath, name string) {
	t.Helper()
	contents, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	fmt.Fprintf(builder, "%x  %s\n", digest, name)
}

func checksumFixture(
	t *testing.T,
) (string, map[target]artifact, map[string]artifact, artifact, []string, []string) {
	t.Helper()
	dist := t.TempDir()
	archives := make(map[target]artifact, len(expectedTargets))
	sboms := make(map[string]artifact, len(expectedTargets))
	lines := make([]string, 0, len(expectedTargets)*2)
	names := make([]string, 0, len(expectedTargets)*2)
	for index, expected := range expectedTargets {
		archiveName := fmt.Sprintf("archive-%d", index)
		sbomName := archiveName + ".sbom.json"
		for _, item := range []artifact{
			{Name: archiveName, Type: "Archive"},
			{Name: sbomName, Type: "SBOM"},
		} {
			contents := []byte("contents-" + item.Name)
			if err := os.WriteFile(filepath.Join(dist, item.Name), contents, 0o644); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(contents)
			lines = append(lines, fmt.Sprintf("%x  %s", digest, item.Name))
			names = append(names, item.Name)
			if item.Type == "Archive" {
				archives[expected] = item
			} else {
				sboms[item.Name] = item
			}
		}
	}
	checksum := artifact{Name: "checksums.txt", Type: "Checksum"}
	if err := os.WriteFile(
		filepath.Join(dist, checksum.Name),
		[]byte(strings.Join(lines, "\n")+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	return dist, archives, sboms, checksum, lines, names
}
