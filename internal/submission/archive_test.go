package submission

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

type testEntry struct {
	name       string
	body       string
	typeflag   byte
	mode       int64
	mtime      time.Time
	paxRecords map[string]string
}

func TestNormalizeTarGzIsDeterministic(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	first := writeTestArchive(t, root, "first.tar.gz", []testEntry{
		{name: "src/main.py", body: "print('hello')\n", typeflag: tar.TypeReg, mode: 0o600, mtime: time.Now()},
		{name: "src/", typeflag: tar.TypeDir, mode: 0o700, mtime: time.Now()},
	})
	second := writeTestArchive(t, root, "second.tar.gz", []testEntry{
		{name: "src/", typeflag: tar.TypeDir, mode: 0o755, mtime: time.Unix(100, 0)},
		{name: "src/main.py", body: "print('hello')\n", typeflag: tar.TypeReg, mode: 0o644, mtime: time.Unix(200, 0)},
	})

	a, err := NormalizeTarGz(first, root, DefaultArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(a.Path) })
	b, err := NormalizeTarGz(second, root, DefaultArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(b.Path) })

	if a.ContentSHA256 != b.ContentSHA256 {
		t.Fatalf("content hashes differ: %s != %s", a.ContentSHA256, b.ContentSHA256)
	}
	if a.FileCount != 1 || a.UncompressedBytes != int64(len("print('hello')\n")) {
		t.Fatalf("unexpected stats: %+v", a)
	}
}

func TestNormalizeTarGzAcceptsGitPAXGlobalHeader(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	archive := writeTestArchive(t, root, "git-archive.tar.gz", []testEntry{
		{name: "pax_global_header", typeflag: tar.TypeXGlobalHeader, paxRecords: map[string]string{"comment": "git archive export"}},
		{name: "src/main.py", body: "print('hello')\n", typeflag: tar.TypeReg},
	})
	normalized, err := NormalizeTarGz(archive, root, DefaultArchiveLimits())
	if err != nil {
		t.Fatalf("NormalizeTarGz() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(normalized.Path) })
	if normalized.FileCount != 1 {
		t.Fatalf("file count = %d, want 1", normalized.FileCount)
	}
}

func TestNormalizeTarGzRejectsUnsafeEntries(t *testing.T) {
	t.Parallel()
	cases := []testEntry{
		{name: "../escape.py", body: "bad", typeflag: tar.TypeReg},
		{name: "/absolute.py", body: "bad", typeflag: tar.TypeReg},
		{name: ".git/config", body: "bad", typeflag: tar.TypeReg},
		{name: "linked", typeflag: tar.TypeSymlink},
	}
	for _, entry := range cases {
		entry := entry
		t.Run(entry.name, func(t *testing.T) {
			archive := writeTestArchive(t, t.TempDir(), "unsafe.tar.gz", []testEntry{entry})
			if _, err := NormalizeTarGz(archive, t.TempDir(), DefaultArchiveLimits()); err == nil {
				t.Fatalf("expected %q to be rejected", entry.name)
			}
		})
	}
}

func TestNormalizeTarGzRejectsLimitsAndDuplicates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	archive := writeTestArchive(t, root, "duplicate.tar.gz", []testEntry{
		{name: "main.py", body: "one", typeflag: tar.TypeReg},
		{name: "main.py", body: "two", typeflag: tar.TypeReg},
	})
	if _, err := NormalizeTarGz(archive, root, DefaultArchiveLimits()); err == nil {
		t.Fatal("expected duplicate path to be rejected")
	}

	large := writeTestArchive(t, root, "large.tar.gz", []testEntry{
		{name: "main.py", body: "12345", typeflag: tar.TypeReg},
	})
	limits := DefaultArchiveLimits()
	limits.MaxFileBytes = 4
	if _, err := NormalizeTarGz(large, root, limits); err == nil {
		t.Fatal("expected oversized file to be rejected")
	}

	directories := writeTestArchive(t, root, "directories.tar.gz", []testEntry{
		{name: "one/", typeflag: tar.TypeDir},
		{name: "two/", typeflag: tar.TypeDir},
		{name: "main.py", body: "ok", typeflag: tar.TypeReg},
	})
	entryLimits := DefaultArchiveLimits()
	entryLimits.MaxEntries = 2
	if _, err := NormalizeTarGz(directories, root, entryLimits); err == nil {
		t.Fatal("expected archive with too many directory entries to be rejected")
	}
}

func TestMaterializeTarGzUsesCanonicalValidationAndHash(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := writeTestArchive(t, root, "source.tar.gz", []testEntry{
		{name: "src/main.py", body: "print('hello')\n", typeflag: tar.TypeReg, mode: 0o755},
	})
	normalized, err := NormalizeTarGz(source, root, DefaultArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(normalized.Path) })

	materialized, err := MaterializeTarGz(normalized.Path, root, DefaultArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(materialized.Path) })
	if materialized.ContentSHA256 != normalized.ContentSHA256 {
		t.Fatalf("content hashes differ: %s != %s", materialized.ContentSHA256, normalized.ContentSHA256)
	}
	data, err := os.ReadFile(filepath.Join(materialized.Path, "src", "main.py"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "print('hello')\n" {
		t.Fatalf("unexpected materialized content: %q", data)
	}
	info, err := os.Stat(filepath.Join(materialized.Path, "src", "main.py"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Fatalf("unexpected executable mode: %o", info.Mode().Perm())
	}
}

func TestMaterializeTarGzCleansPartialTreeAfterUnsafeEntry(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := writeTestArchive(t, root, "unsafe-after-file.tar.gz", []testEntry{
		{name: "valid.txt", body: "valid", typeflag: tar.TypeReg},
		{name: "../escape.txt", body: "bad", typeflag: tar.TypeReg},
	})

	if _, err := MaterializeTarGz(source, root, DefaultArchiveLimits()); err == nil {
		t.Fatal("expected unsafe archive to be rejected")
	}
	matches, err := filepath.Glob(filepath.Join(root, "submission-materialized-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("partial materialization was not cleaned: %v", matches)
	}
}

func TestNormalizeZIPMatchesCanonicalTarContentAndRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	tarArchive := writeTestArchive(t, root, "source.tar.gz", []testEntry{{name: "src/main.py", body: "print('hello')\n", typeflag: tar.TypeReg}})
	zipArchive := writeTestZIP(t, root, "source.zip", map[string]string{"src/main.py": "print('hello')\n"})
	fromTar, err := NormalizeTarGz(tarArchive, root, DefaultArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(fromTar.Path)
	fromZIP, err := NormalizeZIP(zipArchive, root, DefaultArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(fromZIP.Path)
	if fromTar.ContentSHA256 != fromZIP.ContentSHA256 {
		t.Fatalf("canonical content differs: tar=%s zip=%s", fromTar.ContentSHA256, fromZIP.ContentSHA256)
	}
	unsafe := writeTestZIP(t, root, "unsafe.zip", map[string]string{".git/config": "bad"})
	if _, err := NormalizeZIP(unsafe, root, DefaultArchiveLimits()); err == nil {
		t.Fatal("ZIP with .git was accepted")
	}
}

func writeTestZIP(t *testing.T, root, name string, files map[string]string) string {
	t.Helper()
	target := filepath.Join(root, name)
	file, err := os.Create(target)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for path, body := range files {
		entry, err := writer.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return target
}

func writeTestArchive(t *testing.T, root, name string, entries []testEntry) string {
	t.Helper()
	target := filepath.Join(root, name)
	file, err := os.Create(target)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{
			Name:     entry.name,
			Mode:     mode,
			Size:     int64(len(entry.body)),
			Typeflag: entry.typeflag,
			ModTime:  entry.mtime,
			Linkname: "target",
		}
		if entry.typeflag == tar.TypeXGlobalHeader {
			header = &tar.Header{Typeflag: entry.typeflag, PAXRecords: entry.paxRecords}
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if entry.body != "" {
			if _, err := tw.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return target
}
