# photo-bridge

`photo-bridge` is a small, non-destructive archival copy runner built around
[rclone](https://rclone.org/). It gives repeatable copy jobs a value-free YAML
contract, source manifests, one-way verification, locks, stable exit codes, and
redacted JSON reports.

The project is deliberately not a photo gallery and does not reimplement cloud
storage protocols. It treats local disks, NAS mounts, Google Drive, S3,
WebDAV, SFTP, and other rclone backends as configurable endpoints.

## Safety model

- The only v0.1 transfer operation is `rclone copy`.
- Destination-only objects are never removed.
- Source files are never moved or deleted.
- Every run creates a deterministic JSONL source manifest before transfer.
- Verification uses `rclone check --one-way`, so unrelated destination files
  do not fail or disappear.
- Configuration and reports never contain provider credentials.
- Live credentials, media, reports, and personal deployment configuration must
  stay outside this public repository.

## Commands

```text
photo-bridge config validate --config /config/config.yaml
photo-bridge plan --config /config/config.yaml --job example-archive
photo-bridge run --config /config/config.yaml --state-dir /state --job example-archive
photo-bridge run --config /config/config.yaml --state-dir /state --job example-archive --dry-run
photo-bridge run --config /config/config.yaml --state-dir /state --job example-takeout --takeout-ready
photo-bridge verify --config /config/config.yaml --state-dir /state --job example-archive
photo-bridge status --state-dir /state --job example-archive --json
photo-bridge version
```

Stable exit codes:

| Code | Meaning |
|---:|---|
| `0` | Success |
| `2` | Invalid configuration or invocation |
| `3` | Transfer failed or may be partial |
| `4` | Verification failed |
| `5` | Another process holds the job lock |
| `6` | No new complete Takeout export is ready yet |
| `7` | Takeout selection is ambiguous or invalid |
| `10` | Internal/runtime failure |

## Configuration

Start from [`config.example.yaml`](config.example.yaml). Filesystem paths must
be absolute. An rclone endpoint names a remote already defined in a separately
mounted rclone configuration file.

```yaml
apiVersion: photo-bridge/v1alpha1

jobs:
  - name: example-archive
    operation: copy
    source:
      driver: filesystem
      path: /data/input
    destination:
      driver: rclone
      remote: archive-remote
      path: photos/account-a
    policy:
      manifest: sha256
      verification: auto
      transfers: 8
      retries: 3
```

Supported manifest policies:

- `sha256`: require SHA-256. Filesystem sources are read and hashed locally;
  rclone sources must expose SHA-256.
- `auto`: prefer SHA-256, then a deterministic provider hash, then metadata.
- `metadata`: record path, size, and modification time only.

Supported verification policies:

- `auto`: rclone uses a common hash when available and size otherwise.
- `checksum`: require checksum comparison.
- `size`: compare names and sizes only.

Set `PHOTOBRIDGE_RCLONE_CONFIG_FILE` when either endpoint uses the `rclone`
driver. The file must be regular and not world-accessible. The committed
[`secret-contract.yaml`](secret-contract.yaml) describes delivery without
containing values.

## Google Takeout from Drive

For a first complete Google Photos archive, request Google Takeout with
**Add to Drive** and choose the largest practical archive size to reduce the
number of ZIPs. Multiple ZIPs are fully supported.

Use a direct rclone source for cloud-only operation; an rclone FUSE mount is
unnecessary and adds another failure boundary. If a mount is already present,
the same selector works with a `filesystem` source.

```yaml
source:
  driver: rclone
  remote: source-drive
  path: Takeout
  selector:
    kind: google-takeout-latest
    settleFor: 2h
```

A normal scheduled run records the latest candidate and exits `6` until its
exact paths, sizes, and modification times have remained unchanged for the
settle window. After the Google completion email arrives, run once with
`--takeout-ready` to bypass only that wait. It still rejects duplicate names,
invalid numbering, and missing archive parts.

The accepted file set is pinned before copy. Interrupted reruns keep using the
same set even if a newer export appears. A successful verified run marks it
complete; later polls return `6` until another export appears. See
[`docs/takeout-drive-workflow.md`](docs/takeout-drive-workflow.md).

## OCI operation

The image is a one-shot, non-root job runner. It has no scheduler, dashboard,
telemetry, FUSE mount, Docker socket, or inbound listener. Schedule it with
host systemd, a Podman Quadlet timer, or another external job controller.

Examples:

- [`compose.example.yaml`](compose.example.yaml)
- [`deploy/photo-bridge.container.example`](deploy/photo-bridge.container.example)
- [`deploy/photo-bridge.timer.example`](deploy/photo-bridge.timer.example)

OCI images are built only by GitHub Actions. The repository intentionally
blocks local image builds through `just image-build`.

## Google Photos boundary

Google no longer exposes an official unattended API for downloading an
existing full Google Photos library. This project therefore keeps download and
archive transport separate:

1. Run the unmodified community
   [`spraot/gphotos-sync`](https://github.com/spraot/gphotos-sync) container as
   an isolated source job.
2. Point a normal filesystem-source `photo-bridge` job at its download landing
   directory.
3. Keep Google Takeout as the authoritative baseline and album record; the
   Drive selector above handles its archive transport.

The browser profile and any album identifiers are private runtime state. They
must never be committed here. See
[`docs/gphotos-sync-source.md`](docs/gphotos-sync-source.md).

## Development

```text
mise install
just check
just integration
```

Do not put live provider credentials into public CI. Integration tests use only
temporary local files; additional S3 and WebDAV fixtures will use disposable
credential-free containers in later increments.

## Roadmap

- `v0.1`: configurable filesystem/rclone archival copy.
- `v0.2`: documented and canaried `spraot/gphotos-sync` source lane.
- `v0.3`: bulk Google Photos upload through the rclone Google Photos backend.
- `v0.4`: restic snapshots and a Takeout album-membership catalog.

## License

Apache-2.0. See [`LICENSE`](LICENSE).
