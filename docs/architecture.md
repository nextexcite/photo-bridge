# Architecture

## Responsibility split

`photo-bridge` owns the operation contract and evidence around a transfer:

```text
value-free job YAML
        |
        v
validate -> lock -> manifest -> rclone copy -> rclone check --one-way -> report
```

Rclone owns filesystem and cloud protocol behavior. Host systemd or another
external scheduler owns execution timing. A private operations control plane
owns live credentials, job instances, host selection, and recovery.

## Drivers

The v0.1 driver registry is compile-time and deliberately small:

| Driver | Configuration | Transfer implementation |
|---|---|---|
| `filesystem` | Absolute path | Rclone local backend |
| `rclone` | Remote name and provider path | Configured rclone backend |

Adding a new cloud provider normally means configuring another rclone remote,
not adding Go code. A new Go driver is justified only when its semantics cannot
be represented safely through rclone.

## State model

State is inspectable and filesystem-based:

```text
/state/jobs/<job>/
  job.lock
  latest.json
  takeout/
    observation.json
    active.json
    completed.json
  runs/<run-id>/
    manifest.jsonl
    selection.txt
    transfer.log
    verification.log
    report.json
```

Reports refer to state artifacts with paths relative to `/state`. Endpoint
paths and remote names are intentionally omitted from plans and reports.

## Failure model

Rerunning a failed job is the recovery operation. Rclone copy skips unchanged
objects and never removes destination-only objects. A failed transfer receives
exit code `3` because some files may already have completed. Verification is a
separate one-way check and receives exit code `4`.

The source manifest is written before transfer, so an operator can determine
which source view a run attempted to archive.

For a Takeout source, `active.json` is a write-ahead pin. Transfer and one-way
verification both receive the corresponding `--files-from-raw` list. The pin
is replaced by `completed.json` only after verification succeeds. Provider
paths stay in private state and are summarized only as counts and byte totals
in the run report.
