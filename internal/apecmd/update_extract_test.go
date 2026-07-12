package apecmd

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"testing"
)

func TestAssetName(t *testing.T) {
	for _, tc := range []struct {
		goos, goarch, want string
	}{
		{"linux", "amd64", "ape_linux_amd64.tar.gz"},
		{"linux", "arm64", "ape_linux_arm64.tar.gz"},
		{"darwin", "amd64", "ape_darwin_amd64.tar.gz"},
		{"darwin", "arm64", "ape_darwin_arm64.tar.gz"},
		{"windows", "amd64", "ape_windows_amd64.zip"},
		{"windows", "arm64", "ape_windows_arm64.zip"},
	} {
		if got := assetName(tc.goos, tc.goarch); got != tc.want {
			t.Errorf("assetName(%q,%q) = %q, want %q", tc.goos, tc.goarch, got, tc.want)
		}
	}
}

func TestBinaryName(t *testing.T) {
	if got := binaryName("linux"); got != "ape" {
		t.Errorf("binaryName(linux) = %q, want ape", got)
	}
	if got := binaryName("darwin"); got != "ape" {
		t.Errorf("binaryName(darwin) = %q, want ape", got)
	}
	if got := binaryName(goosWindows); got != "ape.exe" {
		t.Errorf("binaryName(windows) = %q, want ape.exe", got)
	}
}

// makeTarGz builds a gzip-compressed tar carrying the given files.
func makeTarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// makeZip builds a zip carrying the given files.
func makeZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestExtractFromTarGz(t *testing.T) {
	want := []byte("\x7fELF fake ape binary")
	archive := makeTarGz(t, map[string][]byte{
		"README.md": []byte("readme"),
		"ape":       want,
		"deploy/x":  []byte("noise"),
	})
	got, err := extractFromTarGz(archive, "ape")
	if err != nil {
		t.Fatalf("extractFromTarGz: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("extracted %q, want %q", got, want)
	}
}

func TestExtractFromZip(t *testing.T) {
	want := []byte("MZ fake ape.exe binary")
	archive := makeZip(t, map[string][]byte{
		"README.md": []byte("readme"),
		"ape.exe":   want,
	})
	got, err := extractFromZip(archive, "ape.exe")
	if err != nil {
		t.Fatalf("extractFromZip: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("extracted %q, want %q", got, want)
	}
}

func TestExtractBinary_DispatchAndMissing(t *testing.T) {
	tgz := makeTarGz(t, map[string][]byte{"ape": []byte("unix")})
	zipArc := makeZip(t, map[string][]byte{"ape.exe": []byte("win")})

	if got, err := extractBinary(tgz, "ape_linux_amd64.tar.gz", "ape"); err != nil || string(got) != "unix" {
		t.Errorf("tar.gz dispatch: got %q err %v", got, err)
	}
	if got, err := extractBinary(zipArc, "ape_windows_amd64.zip", "ape.exe"); err != nil || string(got) != "win" {
		t.Errorf("zip dispatch: got %q err %v", got, err)
	}
	if _, err := extractBinary(tgz, "ape_linux_amd64.tar.gz", "ape.exe"); err == nil {
		t.Error("expected error when binary is absent from archive")
	}
}
