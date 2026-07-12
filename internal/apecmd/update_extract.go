package apecmd

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
)

// goosWindows is the runtime.GOOS value for Windows. Named to keep the .zip
// vs .tar.gz branch readable and to avoid repeating the string literal
// (goconst).
const goosWindows = "windows"

// assetName returns the release-asset filename goreleaser produces for a
// GOOS/GOARCH pair: ape_<goos>_<goarch>.tar.gz, or .zip on Windows. The
// goos/goarch tokens are the raw Go values (linux/darwin/windows,
// amd64/arm64), matching the published asset names exactly.
func assetName(goos, goarch string) string {
	ext := "tar.gz"
	if goos == goosWindows {
		ext = "zip"
	}
	return fmt.Sprintf("ape_%s_%s.%s", goos, goarch, ext)
}

// binaryName is the name of the ape executable inside a release archive for a
// given GOOS (goreleaser appends .exe on Windows).
func binaryName(goos string) string {
	if goos == goosWindows {
		return "ape.exe"
	}
	// "ape" is the executable base name inside the archive — a distinct
	// concept from the unrelated "ape" literals elsewhere in the package (a
	// path segment, a service-name default, the cobra command Use), so a
	// shared constant would wrongly couple them.
	return "ape" //nolint:goconst // program binary base name; distinct from other "ape" strings
}

// extractBinary pulls the named executable out of a release archive, choosing
// the format from the asset filename (.zip → zip, else tar.gz).
func extractBinary(archive []byte, assetFileName, binName string) ([]byte, error) {
	if path.Ext(assetFileName) == ".zip" {
		return extractFromZip(archive, binName)
	}
	return extractFromTarGz(archive, binName)
}

// extractFromTarGz returns the contents of the entry whose base name matches
// binName from a gzip-compressed tar. goreleaser lays the binary at the
// archive root, so a base-name match is sufficient and avoids depending on a
// leading "./".
func extractFromTarGz(archive []byte, binName string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if path.Base(hdr.Name) != binName {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxAssetBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read %s from tar: %w", binName, err)
		}
		if int64(len(data)) > maxAssetBytes {
			return nil, fmt.Errorf("%s in tar exceeds %d byte cap", binName, int64(maxAssetBytes))
		}
		return data, nil
	}
	return nil, fmt.Errorf("%s not found in archive", binName)
}

// extractFromZip returns the contents of the entry whose base name matches
// binName from a zip archive.
func extractFromZip(archive []byte, binName string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || path.Base(f.Name) != binName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s in zip: %w", binName, err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxAssetBytes+1))
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s from zip: %w", binName, err)
		}
		if int64(len(data)) > maxAssetBytes {
			return nil, fmt.Errorf("%s in zip exceeds %d byte cap", binName, int64(maxAssetBytes))
		}
		return data, nil
	}
	return nil, fmt.Errorf("%s not found in archive", binName)
}
