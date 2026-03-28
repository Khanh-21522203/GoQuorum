---
name: Backup & Restore
description: Checkpoint-based point-in-time snapshots with SHA256 integrity verification
type: project
---

# Backup & Restore

## Overview

`internal/backup/backup.go` provides consistent point-in-time snapshots of the Pebble storage engine using Pebble's built-in checkpoint mechanism. Archives are tar.gz files with a JSON manifest containing a SHA256 checksum for integrity verification.

## Backup

`Backup(ctx context.Context, db *pebble.DB, cfg BackupConfig) (*BackupManifest, error)`:

```
1. Create temp directory
2. db.Checkpoint(tempDir, pebble.WithFlushedWAL())
      — Pebble flushes WAL and creates a consistent snapshot in tempDir
      — Snapshot is valid for offline reading without any open transactions
3. createTarGz(ctx, tempDir, destPath)
      destPath = cfg.DestDir/goquorum-backup-<unix_timestamp>.tar.gz
4. sha256File(destPath) → checksum (hex-encoded SHA256)
5. Write manifest JSON:
      cfg.DestDir/goquorum-backup-<unix_timestamp>-manifest.json
      { created_at: <unix_timestamp>, checksum: <hex>, archive_file: <basename> }
6. Return *BackupManifest
```

**`BackupConfig`**: `DestDir string` — local directory for the archive.

**`BackupManifest`**:
```go
type BackupManifest struct {
    CreatedAt   int64   // Unix seconds
    Checksum    string  // SHA256 hex digest of the .tar.gz
    ArchiveFile string  // basename of the archive file
}
```

## Restore

`Restore(ctx context.Context, cfg BackupConfig, archiveFile string, destDataDir string) error`:

```
1. findManifest(cfg.DestDir, archiveFile)
      — naming convention: replace .tar.gz suffix with -manifest.json
2. sha256File(archiveFile) → actualChecksum
3. Compare actualChecksum == manifest.Checksum
      — mismatch: return error (corrupted archive)
4. extractTarGz(ctx, archiveFile, destDataDir)
      — extracts files with mode preservation
      — sanitizes paths (strip leading /, prevent directory traversal)
```

The calling code (e.g., `AdminAPI.RestoreDB` in `backup_api.go`) is responsible for stopping the storage engine before restore and restarting it after.

## Archive Format

`createTarGz(srcDir, destPath)`:
- `filepath.Walk(srcDir)` over all files/dirs.
- Writes tar header with relative paths (strips `srcDir` prefix).
- Gzip-compressed (`gzip.BestCompression`).
- Respects `ctx.Done()` for cancellation during long backups.

`extractTarGz(archivePath, destDir)`:
- Reads gzip-wrapped tar stream.
- For each entry:
  - Path sanitization: strip leading `../` and absolute paths.
  - Creates parent directories (`MkdirAll`).
  - Writes file with original `Mode` bits.

## API Surface (`internal/server/backup_api.go`)

`AdminAPI` methods:

```go
BackupDB(destDir string) (archivePath string, err error)
RestoreDB(archiveFile string, destDataDir string) error
```

These are called via the `GoQuorumAdmin` gRPC service (or from `quorumctl` in future CLI expansion).

## Limitations

- **No incremental backup**: each backup is a full checkpoint.
- **Local destination only**: `DestDir` must be a local filesystem path. S3/GCS not supported.
- **Manual restore process**: caller must stop the node, restore, and restart.
- **In-process checkpointing only**: `db *pebble.DB` is passed directly; no remote backup agent.

Changes:

