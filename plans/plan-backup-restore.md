# Feature: Backup & Restore

## 1. Purpose

GoQuorum currently has no mechanism for taking a consistent snapshot of a node's data or restoring from one. Without backup and restore, recovering from catastrophic storage failure requires waiting for anti-entropy to rebuild the node's data from surviving replicas — which is slow and only works if at least one other replica holds each key. Backup and restore provides a faster, operator-controlled recovery path: take a snapshot of node data at a point in time, upload it to object storage, and restore it to a replacement node. This feature is not yet implemented.

## 2. Responsibilities

- **Snapshot creation**: Use Pebble's built-in checkpoint API to create a consistent, point-in-time snapshot of the local storage directory without blocking reads or writes
- **Snapshot upload**: Stream the snapshot files to a configurable object storage backend (local filesystem, S3-compatible)
- **Snapshot listing**: List available snapshots with metadata (timestamp, node ID, file size)
- **Restore**: Download a snapshot from object storage, replace the local storage directory, and restart the engine
- **Admin API**: Expose `CreateBackup`, `ListBackups`, and `RestoreBackup` via the admin gRPC/HTTP API
- **Incremental hints**: Include node metadata (node ID, snapshot timestamp, key count) in the backup manifest so the restoring node can validate and report progress

## 3. Non-Responsibilities

- Does not implement cluster-wide consistent snapshots across all nodes (each node is backed up independently; cross-node consistency is eventual via anti-entropy)
- Does not implement automatic scheduled backups (operator uses cron or external scheduler)
- Does not implement backup encryption (operator encrypts at the object storage layer)
- Does not implement replication of backups across regions (delegated to object storage)

## 4. Architecture Design

```
Admin API: POST /v1/admin/backup
    |
    v
BackupManager.CreateBackup(ctx, destination)
    |
    +-- Pebble.Checkpoint(checkpointDir)   // atomic snapshot, no write pause
    |
    +-- walk checkpointDir files
    |
    +-- upload each file to ObjectStore (e.g., S3 bucket)
    |     with prefix: "backups/<nodeID>/<timestamp>/"
    |
    +-- upload manifest.json:
          { node_id, timestamp, key_count, files: [...] }
    |
    v
return BackupID (= "<nodeID>/<timestamp>")

Admin API: POST /v1/admin/restore (body: {backup_id, destination})
    |
    v
BackupManager.RestoreBackup(ctx, backupID)
    |
    +-- download all files from ObjectStore/<backupID>/
    +-- stop storage engine
    +-- replace data dir with restored files
    +-- restart storage engine
    +-- rebuild Merkle tree from restored data
```

## 5. Core Data Structures (Go)

```go
package backup

import (
    "context"
    "io"
    "time"
)

// BackupManager coordinates backup and restore operations.
type BackupManager struct {
    config   BackupConfig
    engine   storage.Engine          // for Checkpoint and Close/Reopen
    pebbleDB *pebble.DB              // direct access for Checkpoint API
    store    ObjectStore
    nodeID   string
    dataDir  string
    logger   *slog.Logger
    metrics  *BackupMetrics
}

// BackupManifest describes one backup snapshot.
type BackupManifest struct {
    BackupID    string    // "<nodeID>/<timestamp>"
    NodeID      string
    Timestamp   time.Time
    KeyCount    int64
    SizeBytes   int64
    Files       []BackupFile
}

// BackupFile describes one file in a backup.
type BackupFile struct {
    Name      string
    SizeBytes int64
    Checksum  string // SHA256 hex
}

// ObjectStore is the interface for backup storage (local or S3-compatible).
type ObjectStore interface {
    // Upload streams data to the given path.
    Upload(ctx context.Context, path string, r io.Reader, size int64) error
    // Download streams data from the given path.
    Download(ctx context.Context, path string) (io.ReadCloser, error)
    // List returns all object paths with the given prefix.
    List(ctx context.Context, prefix string) ([]string, error)
    // Delete removes the object at path.
    Delete(ctx context.Context, path string) error
}

// BackupConfig holds backup/restore configuration.
type BackupConfig struct {
    // StorageType selects the ObjectStore implementation: "local" or "s3".
    StorageType string `yaml:"storage_type" default:"local"`
    // LocalPath is the directory for local backup storage.
    LocalPath string `yaml:"local_path"`
    // S3Bucket is the S3 bucket name for S3 storage.
    S3Bucket string `yaml:"s3_bucket"`
    // S3Endpoint is the S3 endpoint URL (empty = AWS default).
    S3Endpoint string `yaml:"s3_endpoint"`
    // S3Region is the AWS region.
    S3Region string `yaml:"s3_region"`
    // UploadConcurrency is the number of parallel file uploads.
    UploadConcurrency int `yaml:"upload_concurrency" default:"4"`
}
```

## 6. Public Interfaces

```go
package backup

// NewBackupManager creates a BackupManager.
func NewBackupManager(cfg BackupConfig, db *pebble.DB, eng storage.Engine, nodeID, dataDir string, logger *slog.Logger) *BackupManager

// CreateBackup creates a Pebble checkpoint and uploads it to object storage.
// Returns the BackupID on success.
func (m *BackupManager) CreateBackup(ctx context.Context) (string, error)

// ListBackups returns all available backups for this node, sorted newest first.
func (m *BackupManager) ListBackups(ctx context.Context) ([]BackupManifest, error)

// RestoreBackup downloads backupID and replaces the local data directory.
// Requires stopping the storage engine before calling.
// After RestoreBackup returns, the engine must be reopened by the caller.
func (m *BackupManager) RestoreBackup(ctx context.Context, backupID string) error

// NewLocalObjectStore creates a filesystem-backed ObjectStore.
func NewLocalObjectStore(rootDir string) ObjectStore

// NewS3ObjectStore creates an S3-backed ObjectStore.
func NewS3ObjectStore(cfg BackupConfig) (ObjectStore, error)

// Admin proto extension:
// service GoQuorumAdmin {
//   rpc CreateBackup(CreateBackupRequest) returns (CreateBackupResponse);
//   rpc ListBackups(ListBackupsRequest) returns (ListBackupsResponse);
//   rpc RestoreBackup(RestoreBackupRequest) returns (RestoreBackupResponse);
// }
```

## 7. Internal Algorithms

### CreateBackup
```
CreateBackup(ctx):
  timestamp = now()
  backupID = nodeID + "/" + timestamp.Format(RFC3339)
  checkpointDir = dataDir + "/.checkpoint_" + timestamp.Unix()

  // Create Pebble checkpoint (atomic, non-blocking)
  err = pebbleDB.Checkpoint(checkpointDir)
  if err != nil: return "", err

  defer os.RemoveAll(checkpointDir)  // cleanup temp dir after upload

  // Enumerate files and compute checksums
  files = walkDir(checkpointDir)
  manifest = BackupManifest{
    BackupID: backupID, NodeID: nodeID, Timestamp: timestamp,
    KeyCount: engine.DiskUsage().LiveKeyCount,
    Files: files,
  }

  // Upload files in parallel (semaphore-controlled)
  sem = make(chan struct{}, config.UploadConcurrency)
  for each file in files:
    sem <- struct{}{}
    go func():
      defer func() { <-sem }()
      remotePath = "backups/" + backupID + "/" + file.Name
      store.Upload(ctx, remotePath, openFile(checkpointDir, file.Name), file.SizeBytes)
  drain(sem)

  // Upload manifest last (presence of manifest = backup complete)
  manifestJSON = json.Marshal(manifest)
  store.Upload(ctx, "backups/"+backupID+"/manifest.json", manifestJSON, len(manifestJSON))

  metrics.BackupsCreated.Inc()
  return backupID, nil
```

### RestoreBackup
```
RestoreBackup(ctx, backupID):
  // Download manifest
  r, err = store.Download(ctx, "backups/"+backupID+"/manifest.json")
  manifest = json.Decode(r, &BackupManifest)

  // Create temp restore dir
  restoreDir = dataDir + "/.restore_" + timestamp.Unix()
  os.MkdirAll(restoreDir)

  // Download all files
  sem = make(chan struct{}, config.UploadConcurrency)
  for each file in manifest.Files:
    sem <- struct{}{}
    go func():
      defer func() { <-sem }()
      r, _ = store.Download(ctx, "backups/"+backupID+"/"+file.Name)
      writeFile(restoreDir, file.Name, r)
      verifyChecksum(restoreDir+"/"+file.Name, file.Checksum)
  drain(sem)

  // Atomically swap data directories
  os.Rename(dataDir, dataDir+".bak."+timestamp.Unix())
  os.Rename(restoreDir, dataDir)

  metrics.RestoresCompleted.Inc()
  return nil
  // Caller is responsible for reopening the storage engine
```

## 8. Persistence Model

Backups are stored in object storage (local filesystem or S3). Each backup consists of:
- `backups/<nodeID>/<timestamp>/manifest.json` — metadata and file list
- `backups/<nodeID>/<timestamp>/*.sst` — Pebble SST files from the checkpoint
- `backups/<nodeID>/<timestamp>/MANIFEST*` — Pebble manifest
- `backups/<nodeID>/<timestamp>/OPTIONS*` — Pebble options

Pebble's checkpoint API hard-links SST files (on local disk) to avoid copying; uploads stream the actual data.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| Pebble checkpoint API | Non-blocking; does not pause reads or writes; creates a consistent snapshot |
| Parallel upload goroutines | `UploadConcurrency` goroutines upload files in parallel |
| Engine stop/start | Restore requires stopping the engine (no writes during restore); caller responsible |
| Admin API serialization | Only one backup or restore runs at a time (enforced by an `atomic.Bool` in BackupManager) |

## 10. Configuration

```go
type BackupConfig struct {
    StorageType       string `yaml:"storage_type" default:"local"`
    LocalPath         string `yaml:"local_path"`
    S3Bucket          string `yaml:"s3_bucket"`
    S3Endpoint        string `yaml:"s3_endpoint"`
    S3Region          string `yaml:"s3_region"`
    UploadConcurrency int    `yaml:"upload_concurrency" default:"4"`
}
```

## 11. Observability

- `goquorum_backup_created_total` — Counter of successful backups
- `goquorum_backup_failed_total` — Counter of failed backup attempts
- `goquorum_backup_duration_seconds` — Histogram of backup creation duration
- `goquorum_backup_size_bytes` — Histogram of backup total upload size
- `goquorum_restore_completed_total` — Counter of successful restores
- `goquorum_restore_failed_total` — Counter of failed restore attempts
- Log at INFO when backup starts and completes (with backup ID and size)
- Log at ERROR on upload failure with filename and error

## 12. Testing Strategy

- **Unit tests**:
  - `TestCreateBackupProducesManifest`: mock ObjectStore, call CreateBackup, assert manifest uploaded
  - `TestCreateBackupUploadsAllFiles`: assert all checkpoint files uploaded
  - `TestRestoreBackupDownloadsAllFiles`: mock ObjectStore, call RestoreBackup, assert all files written to restoreDir
  - `TestRestoreVerifiesChecksum`: corrupt one file, assert RestoreBackup returns checksum error
  - `TestConcurrentBackupPrevented`: two concurrent CreateBackup calls, assert second returns error
  - `TestLocalObjectStoreUploadDownload`: upload and download a file, assert byte equality
- **Integration tests**:
  - `TestBackupRestoreCycle`: start node, write 1000 keys, backup, stop, delete data dir, restore, restart, assert all 1000 keys readable
  - `TestBackupDuringWrites`: write continuously while backup runs, assert backup completes without error

## 13. Open Questions

- Should we support cluster-wide backup (coordinator instructs all N nodes to backup simultaneously for a consistent cross-node snapshot)?
- Should we implement incremental backups (upload only changed SST files since last backup)?
