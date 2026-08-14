package backup

import (
	"context"

	"goquorum.io/v2/contracts"
	pebblestore "goquorum.io/v2/infra/storage/pebble"
)

// Config holds backup/restore configuration.
//
// (v1: internal/backup/backup.go BackupConfig)
type Config struct {
	DestDir string // Local directory to write the backup archive.
}

// Manifest is written alongside the backup archive, describing it.
//
// (v1: internal/backup/backup.go BackupManifest)
type Manifest struct {
	CreatedAt   int64  // Unix seconds.
	Checksum    string // SHA256 of the .tar.gz archive.
	ArchiveFile string // Basename of the archive file.
}

// Backup creates a consistent Pebble checkpoint of store and archives it
// as a checksummed .tar.gz under cfg.DestDir.
//
// v1 took a *pebble.DB directly; engine/storage.Storage (the port) does
// not expose one (see engine/storage.Storage's doc comment). Once Pebble
// is wired in, Store should grow its own DB() *pebble.DB escape hatch so
// this function can checkpoint it.
//
// TODO(v2): import github.com/cockroachdb/pebble, archive/tar,
// compress/gzip, crypto/sha256, os; call store.DB().Checkpoint(...) into a
// temp dir, tar+gzip it into cfg.DestDir, checksum the archive, and write
// the manifest JSON alongside it (v1: internal/backup/backup.go Backup).
func Backup(ctx context.Context, store *pebblestore.Store, cfg Config) (*Manifest, error) {
	return nil, contracts.ErrNotImplemented
}

// Restore verifies and extracts a backup archive into destDataDir.
//
// TODO(v2): import archive/tar, compress/gzip, crypto/sha256, os; find the
// manifest for archiveFile, verify its checksum, and extract the archive
// into destDataDir (v1: internal/backup/backup.go Restore).
func Restore(ctx context.Context, cfg Config, archiveFile, destDataDir string) error {
	return contracts.ErrNotImplemented
}
