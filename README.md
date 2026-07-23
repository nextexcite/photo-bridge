# photo-bridge

`photo-bridge` is a small, non-destructive archival-copy runner built around
[rclone](https://rclone.org/). It gives repeatable jobs a value-free YAML
contract, pinned manifests, one-way verification, locks, redacted reports, and
terminal progress.

It is not a photo gallery and does not reimplement storage protocols. Local
disks, NAS mounts, Google Drive, S3, WebDAV, SFTP, and other rclone backends
are endpoints configured outside this public repository.

## Safety model

- The only transfer operation is `rclone copy`: it never removes, moves, or
  synchronizes deletions.
- Source identities are pinned by path, size, modification time, and available
  hashes. They are revalidated before copy and before verification.
- Destination-only objects are not checked as failures and are never removed.
- Reports and plans contain safe counts and methods, not provider identities,
  object names, or credentials. Private state can still reveal timing and
  filenames and must remain private.
- `v1alpha2` is a hard cut. `v1alpha1` YAML, reports, and state roots are
  rejected; do not point this release at a legacy state directory.

## Commands

```text
photo-bridge config validate --config /config/config.yaml
photo-bridge plan --config /config/config.yaml --job example-archive
photo-bridge run --config /config/config.yaml --state-dir /state --job example-archive
photo-bridge run --config /config/config.yaml --state-dir /state --job example-archive --dry-run
photo-bridge run --config /config/config.yaml --state-dir /state --job example-takeout --takeout-ready
photo-bridge verify --config /config/config.yaml --state-dir /state --job example-archive
photo-bridge status --state-dir /state --job example-archive --json
photo-bridge state prune --state-dir /state --job example-archive --dry-run --json
photo-bridge version
```

`run` and `verify` write exactly one JSON report to stdout. Sanitized human
progress is written to stderr, suitable for a terminal or service journal:

```text
photo-bridge: phase=transfer progress=50.0% transferred=5.00GiB total=10.00GiB speed=20.00MiB/s eta=4m16s errors=0
```

`status --json` returns `current.json` while a job is active, including phase,
progress, speed, ETA, and last-update time. When idle it returns `latest.json`.

Stable exit codes:

| Code | Meaning |
|---:|---|
| `0` | Success |
| `2` | Invalid configuration or invocation |
| `3` | Transfer failed or may be partial |
| `4` | Verification or required checksum capability failed |
| `5` | Another process holds the job lock |
| `6` | No new complete Takeout export is ready yet |
| `7` | Takeout selection or pinned identity is invalid |
| `8` | Configured object or byte limit exceeded before transfer |
| `9` | Maximum job duration exceeded |
| `10` | Internal/runtime failure |

## `v1alpha2` configuration

Start with [`config.example.yaml`](config.example.yaml). Filesystem paths are
absolute. An rclone endpoint names a remote that is already present in a
separately mounted rclone config.

```yaml
apiVersion: photo-bridge/v1alpha2

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
      integrity:
        manifest: auto
        verification: auto
        allowEmpty: false
      transfer:
        transfers: 8
        checkers: 8
        bufferSize: 16MiB
        maxBufferMemory: 256MiB
      limits:
        maxDuration: 0s
        maxFiles: 0
        maxBytes: 0
      progressInterval: 5s
      retention:
        mode: automatic
        maxAge: 720h
        minRuns: 5
```

Verification is resolved before any destination write:

- `checksum` requires a common advertised hash or fails.
- `auto` uses a common hash when possible, otherwise records an explicit
  size/name fallback.
- `size` compares pinned paths and sizes.
- `download` reads both sides and compares contents; use it only when its
  extra provider bandwidth is acceptable.

`maxBufferMemory` bounds rclone transfer buffers, not total process RSS. No
VFS or disk cache is enabled. Zero limits mean unlimited. `maxFiles` and
`maxBytes` are checked before destination writes. `maxDuration` ends the run
with exit `9` while preserving resumable state.

Automatic retention preserves every run less than `maxAge` old and always the
newest `minRuns` per job. It never removes active state, Takeout pins,
`latest.json`, credentials, source data, or destination data. Use `state
prune --dry-run --json` to preview maintenance; retention warnings do not
change a transfer result.

Set `PHOTOBRIDGE_RCLONE_CONFIG_FILE` when either endpoint uses the `rclone`
driver. It must be a regular, non-world-readable runtime-mounted file. The
committed [`secret-contract.yaml`](secret-contract.yaml) describes delivery
without containing values.

## Data path

`plan` reports an intentionally high-level transport classification:

| `dataPath` | Meaning |
|---|---|
| `host-local` | Both endpoints are host-local filesystem paths. |
| `host-upload` | Local source streams to a remote destination. |
| `host-download` | Remote source streams to local storage. |
| `host-relay` | Bytes stream through the job host between remote endpoints. |

`serverSideCopy` is `false` in this release. A remote-to-remote job is a
host-relay: its host consumes both ingress and egress bandwidth but does not
stage a media cache on disk.

## Google Takeout from Drive

For a first full Google Photos archive, request Google Takeout with **Add to
Drive** and choose the largest practical archive size. One ZIP is preferred;
numbered sets are supported.

```yaml
source:
  driver: rclone
  remote: source-drive
  path: Takeout
  selector:
    kind: google-takeout-latest
    settleFor: 2h
```

A normal scheduled run observes the newest candidate and exits `6` until its
exact identity is stable for the settle window. `--takeout-ready` bypasses only
that wait after the provider completion signal; structural and identity checks
remain mandatory. The selected set is private active state, reused after an
interruption, and only marked complete after verification. See
[`docs/takeout-drive-workflow.md`](docs/takeout-drive-workflow.md).

## OCI operation and releases

The image is a one-shot, non-root job runner: no scheduler, dashboard,
telemetry, FUSE mount, Docker socket, or inbound listener. Host systemd,
Podman Quadlet, or another external controller owns scheduling.

- [`compose.example.yaml`](compose.example.yaml)
- [`deploy/photo-bridge.container.example`](deploy/photo-bridge.container.example)
- [`deploy/photo-bridge.timer.example`](deploy/photo-bridge.timer.example)

Images are built only by GitHub Actions. Operators use exact SemVer tags, for
example `ghcr.io/nextexcite/photo-bridge:0.1.3`; a digest, SBOM, and provenance
are integrity evidence and rollback coordinates. The release pipeline builds
each architecture once, scans and smokes those candidate digests, creates the
multi-architecture index, attests it, promotes SemVer channels, then creates
the GitHub Release. A failed scan never produces a release.

From clean, up-to-date `main`:

```text
mise exec -- just release v0.1.3
```

## Development

```text
mise install
just check
just integration
just integration-backends
```

Public CI has no provider credentials. The backend integration lane uses
disposable local MinIO and WebDAV services. `just public-audit` runs gitleaks
against the worktree and complete history, validates commit metadata, rejects
unsafe artifact shapes, and includes adversarial self-tests.

## License

Apache-2.0. See [`LICENSE`](LICENSE).
