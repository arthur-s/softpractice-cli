package starterbundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildAndExtractAreReproducibleAndSafe(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	first := []byte("# starter\n")
	second := []byte("print('hello')\n")
	if err := os.WriteFile(filepath.Join(root, "README.md"), first, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.py"), second, 0o600); err != nil {
		t.Fatal(err)
	}
	files := []File{{Path: "README.md", SHA256: sha256Hex(first)}, {Path: "src/main.py", SHA256: sha256Hex(second)}}
	var one, two bytes.Buffer
	artifact, err := Build(root, files, &one)
	if err != nil {
		t.Fatal(err)
	}
	secondArtifact, err := Build(root, files, &two)
	if err != nil {
		t.Fatal(err)
	}
	if artifact != secondArtifact || !bytes.Equal(one.Bytes(), two.Bytes()) {
		t.Fatal("starter bundle is not reproducible")
	}
	destination := filepath.Join(t.TempDir(), "starter")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Extract(bytes.NewReader(one.Bytes()), destination, artifact.SHA256, artifact.Size); err != nil {
		t.Fatal(err)
	}
	for path, expected := range map[string][]byte{"README.md": first, "src/main.py": second} {
		actual, err := os.ReadFile(filepath.Join(destination, path))
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("extracted %s = %q, %v", path, actual, err)
		}
	}
	if err := Extract(bytes.NewReader(one.Bytes()), destination, artifact.SHA256, artifact.Size); err == nil {
		t.Fatal("non-empty destination was accepted")
	}
}

func TestBuildRejectsManifestMismatch(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.py"), []byte("print(1)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(root, []File{{Path: "main.py", SHA256: "0000000000000000000000000000000000000000000000000000000000000000"}}, &bytes.Buffer{}); err == nil {
		t.Fatal("mismatched manifest checksum was accepted")
	}
}

func TestBuildZIPIsReproducible(t *testing.T) {
	root := t.TempDir()
	contents := []byte("print('hello')\n")
	if err := os.WriteFile(filepath.Join(root, "main.py"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	files := []File{{Path: "main.py", SHA256: sha256Hex(contents)}}
	var first, second bytes.Buffer
	one, err := BuildZIP(root, files, &first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := BuildZIP(root, files, &second)
	if err != nil {
		t.Fatal(err)
	}
	if one != two || !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("starter ZIP is not reproducible")
	}
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
