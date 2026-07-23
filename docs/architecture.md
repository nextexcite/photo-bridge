# Architecture

## Responsibility split

`photo-bridge` owns the copy contract and evidence around one job:

```text
validate -> lock -> pin identities -> manifest -> copy -> verify -> report -> retain state
```

Rclone owns filesystem and cloud protocols. An external scheduler owns timing.
A private operations control plane owns live credentials, job instances, host
selection, and recovery.

## Drivers and data path

The compile-time driver registry is deliberately small:

| Driver | Configuration | Transfer implementation |
|---|---|---|
| `filesystem` | Absolute path | rclone local backend |
| `rclone` | Remote name and provider path | configured rclone backend |

Provider additions normally mean another rclone remote, not Go code. The plan
does not expose endpoint names. It instead classifies the physical data path as
`host-local`, `host-upload`, `host-download`, or `host-relay`. All remote to
remote copies currently use `host-relay`; `serverSideCopy` remains false.

## `v1alpha2` state model

State is private, inspectable, and schema-marked. A nonempty root that is not a
`v1alpha2` root is rejected rather than interpreted as legacy state.

```text
/state/
  schema.json
  jobs/<job>/
    job.lock
    current.json
    latest.json
    takeout/
      observation.json
      active.json
      completed.json
    runs/<run-id>/
      manifest.jsonl
      selection.jsonl
      transfer.log
      verification.log
      report.json
      spool/                 # temporary bounded-manifest chunks
```

`current.json` is updated at the configured progress cadence and removed only
after a terminal report is written. A lock-less current state is reconciled as
interrupted on the next invocation. Reports use
`photo-bridge.report/v1alpha2`, have immutable run IDs, and include only safe
counts, timings, data-path classification, requested/effective verification,
and retention summary.

## Integrity and recovery

Selected objects have a persisted identity: path, size, modification time, and
any available provider hashes. The runner revalidates the identity before copy
and verification. A changed or missing object fails rather than silently
mixing an export. `checksum` requires a common hash; `auto` reports an explicit
fallback when only size/name comparison is possible.

Rerunning a failed copy is the recovery operation. `rclone copy` skips
unchanged objects and never removes destination-only objects. The source
manifest is written before transfer, so an operator can determine the exact
source view attempted without placing source identities in public reports.

For a Takeout source, `active.json` is a write-ahead pin. Both copy and
verification receive its filtered list. It becomes `completed.json` only after
successful verification. Retention never removes any Takeout selector state.

## Resource boundaries

The manifest pipeline streams source listings. It holds at most 64 MiB of
encoded metadata before spilling sorted chunks under the run directory, then
merges deterministic JSONL and removes spools on every terminal path. Rclone
transfer settings are structured configuration, not arbitrary arguments.
`maxBufferMemory` limits rclone buffers rather than total process RSS; disk and
VFS caches are disabled.
