//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// copyFile copies the file at srcFile to dstFile, creating any
// necessary parent directories. The destination file permissions
// are set to 0o600.
func copyFile(srcFile, dstFile string) error {
	// Ensure the destination directory exists.
	if err := os.MkdirAll(filepath.Dir(dstFile), 0o750); err != nil {
		return fmt.Errorf("failed to create directory for %s: %w", dstFile, err)
	}

	in, err := os.Open(srcFile)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dstFile)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}

	return out.Sync()
}

// writeFile writes content to the given path, creating any necessary
// parent directories. File permissions are set to 0o600.
func writeFile(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("failed to create directory for %s: %w", path, err)
	}

	return os.WriteFile(path, body, 0o600)
}
