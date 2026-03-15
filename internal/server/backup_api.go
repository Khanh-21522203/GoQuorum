package server

import (
	"context"
	"fmt"
	"path/filepath"

	"GoQuorum/internal/backup"
)

// BackupDB creates a backup of the storage database to destDir.
// It returns the path to the created archive file.
func (a *AdminAPI) BackupDB(destDir string) (string, error) {
	if a.storage == nil {
		return "", fmt.Errorf("storage not initialized")
	}

	cfg := backup.BackupConfig{DestDir: destDir}
	manifest, err := backup.Backup(context.Background(), a.storage.DB(), cfg)
	if err != nil {
		return "", fmt.Errorf("backup failed: %w", err)
	}

	archivePath := filepath.Join(destDir, manifest.ArchiveFile)
	return archivePath, nil
}

// RestoreDB restores a backup archive into destDataDir.
// archiveFile is the basename of the .tar.gz file inside cfg.DestDir (the
// directory that was used when the backup was created and now holds both the
// archive and its manifest).
func (a *AdminAPI) RestoreDB(archiveFile, destDataDir string) error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}

	// Derive the backup directory from the archive file path.
	// Callers pass the full path; we split dir and base here for convenience.
	dir := filepath.Dir(archiveFile)
	base := filepath.Base(archiveFile)

	cfg := backup.BackupConfig{DestDir: dir}
	return backup.Restore(context.Background(), cfg, base, destDataDir)
}
