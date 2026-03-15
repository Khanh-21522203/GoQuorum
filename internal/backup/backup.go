package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/cockroachdb/pebble"
)

// BackupConfig holds backup/restore configuration.
type BackupConfig struct {
	DestDir string // local directory to write the backup archive
}

// BackupManifest is written as backup-manifest.json alongside the archive.
type BackupManifest struct {
	CreatedAt   int64  `json:"created_at"`   // Unix seconds
	Checksum    string `json:"checksum"`     // SHA256 of the .tar.gz archive
	ArchiveFile string `json:"archive_file"` // basename of the archive file
}

// Backup creates a Pebble checkpoint then tars+gzips it.
// Returns the manifest describing the created backup.
func Backup(ctx context.Context, db *pebble.DB, cfg BackupConfig) (*BackupManifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(cfg.DestDir, 0o755); err != nil {
		return nil, fmt.Errorf("create dest dir: %w", err)
	}

	// Create a temp dir for the checkpoint
	checkpointDir, err := os.MkdirTemp("", "goquorum-checkpoint-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir for checkpoint: %w", err)
	}
	defer os.RemoveAll(checkpointDir)

	// Create a Pebble checkpoint (consistent snapshot with flushed WAL)
	if err := db.Checkpoint(checkpointDir, pebble.WithFlushedWAL()); err != nil {
		return nil, fmt.Errorf("pebble checkpoint: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Build the archive filename
	ts := time.Now().Unix()
	archiveName := fmt.Sprintf("goquorum-backup-%d.tar.gz", ts)
	archivePath := filepath.Join(cfg.DestDir, archiveName)

	// Create tar.gz archive of the checkpoint dir
	if err := createTarGz(ctx, checkpointDir, archivePath); err != nil {
		return nil, fmt.Errorf("create tar.gz: %w", err)
	}

	// Compute SHA256 of the archive
	checksum, err := sha256File(archivePath)
	if err != nil {
		return nil, fmt.Errorf("compute checksum: %w", err)
	}

	manifest := &BackupManifest{
		CreatedAt:   ts,
		Checksum:    checksum,
		ArchiveFile: archiveName,
	}

	// Write manifest JSON alongside the archive
	manifestPath := filepath.Join(cfg.DestDir, fmt.Sprintf("goquorum-backup-%d-manifest.json", ts))
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o644); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}

	return manifest, nil
}

// Restore extracts a backup archive into destDataDir.
// It verifies the SHA256 checksum from the manifest before extracting.
func Restore(ctx context.Context, cfg BackupConfig, archiveFile, destDataDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	archivePath := filepath.Join(cfg.DestDir, archiveFile)

	// Find and read the manifest for this archive
	manifest, err := findManifest(cfg.DestDir, archiveFile)
	if err != nil {
		return fmt.Errorf("find manifest: %w", err)
	}

	// Verify checksum
	checksum, err := sha256File(archivePath)
	if err != nil {
		return fmt.Errorf("compute checksum for verification: %w", err)
	}
	if checksum != manifest.Checksum {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", manifest.Checksum, checksum)
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	// Ensure destination directory exists and is empty (or create it)
	if err := os.MkdirAll(destDataDir, 0o755); err != nil {
		return fmt.Errorf("create dest data dir: %w", err)
	}

	// Extract tar.gz into destDataDir
	if err := extractTarGz(ctx, archivePath, destDataDir); err != nil {
		return fmt.Errorf("extract tar.gz: %w", err)
	}

	return nil
}

// createTarGz walks srcDir and writes all files into a .tar.gz at destPath.
func createTarGz(ctx context.Context, srcDir, destPath string) error {
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create archive file: %w", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		// Compute the name relative to srcDir so the archive has relative paths
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return fmt.Errorf("compute relative path: %w", err)
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("create tar header: %w", err)
		}
		header.Name = relPath

		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("write tar header: %w", err)
		}

		if info.IsDir() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open file for archiving: %w", err)
		}
		defer file.Close()

		if _, err := io.Copy(tw, file); err != nil {
			return fmt.Errorf("write file to archive: %w", err)
		}

		return nil
	})
}

// extractTarGz extracts a .tar.gz archive into destDir.
func extractTarGz(ctx context.Context, archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("create gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		// Sanitize the path to prevent directory traversal
		targetPath := filepath.Join(destDir, filepath.Clean("/"+header.Name))

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("create dir %s: %w", targetPath, err)
			}
		case tar.TypeReg:
			// Ensure parent directory exists
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return fmt.Errorf("create parent dir for %s: %w", targetPath, err)
			}
			outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("create file %s: %w", targetPath, err)
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return fmt.Errorf("write file %s: %w", targetPath, err)
			}
			outFile.Close()
		}
	}

	return nil
}

// sha256File computes the hex-encoded SHA256 checksum of the file at path.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file for checksum: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("compute sha256: %w", err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// findManifest locates the manifest JSON file that corresponds to archiveFile
// in the given directory. It looks for a file named after the convention used
// in Backup: <base-without-.tar.gz>-manifest.json.
func findManifest(dir, archiveFile string) (*BackupManifest, error) {
	// Try the naming convention: replace ".tar.gz" with "-manifest.json"
	base := archiveFile
	if len(base) > 7 && base[len(base)-7:] == ".tar.gz" {
		base = base[:len(base)-7]
	}
	manifestName := base + "-manifest.json"
	manifestPath := filepath.Join(dir, manifestName)

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", manifestPath, err)
	}

	var manifest BackupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	return &manifest, nil
}
