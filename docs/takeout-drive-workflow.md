# Google Takeout through Drive

This lane automates discovery, transfer, retries, and evidence. Google still
owns export creation, and the completion email is the strongest available
signal that the provider has finished producing the files.

## One-time setup

1. In Google Takeout, select Google Photos, choose **Add to Drive**, and choose
   the largest practical archive size. Prefer one ZIP, but do not depend on it.
2. Configure an rclone Drive remote outside this repository. Give the runtime
   read access to the source and write access only to the intended destination.
3. Create a private job from the synthetic `example-takeout` configuration.
4. Schedule `photo-bridge run` from systemd or a Quadlet timer. Treat exit `6`
   as success because polling before completion is expected.

Direct remote access is preferred for a server-side cloud transfer:

```yaml
source:
  driver: rclone
  remote: source-drive
  path: Takeout
  selector:
    kind: google-takeout-latest
    settleFor: 2h
```

An existing rclone mount can instead be expressed as a filesystem endpoint:

```yaml
source:
  driver: filesystem
  path: /data/source-drive/Takeout
  selector:
    kind: google-takeout-latest
    settleFor: 2h
```

## Normal operation

The first poll observes the newest timestamped Takeout ZIP or numbered ZIP
set. A later poll accepts it only when the exact paths, sizes, and modification
times have been stable for `settleFor`. This avoids copying a provider file
that is still changing, but it cannot prove that Google intends no additional
part.

When the completion email arrives, an operator can use the stronger signal:

```text
photo-bridge run --job example-takeout --takeout-ready
```

This flag bypasses the settle timer. It does not bypass structural checks. The
runner refuses duplicate logical filenames, part numbers below one, duplicate
part numbers, and numeric sequences with gaps.

Before transfer, the accepted list is stored as private job state with each
object's path, size, modification time, and available hash. Both copy and
verification are filtered to that list. The runner revalidates it before copy
and verification; a changed source fails rather than mixing a newer export.
If the network, process, or host fails, the next run retries the same pinned
list. Rclone copy safely skips files that already completed.

After successful one-way verification, the pin becomes the completed marker.
Polling the same export then returns exit `6` and status `up_to_date`.

## Operator recovery

- `waiting`: wait for the email, wait for the settle window, or inspect the
  provider UI. No destination writes occurred.
- `selection_failed`: inspect source Drive for duplicate names or a missing
  numbered part. Do not force the copy; repair or disambiguate the source.
- `transfer_failed`: rerun normally. The pinned set is retained.
- `verification_failed`: keep the pin and rerun; review the private redacted
  logs if repeated attempts fail.

Do not edit `active.json` casually. It is the recovery boundary that prevents
one logical run from mixing two exports. `v1alpha2` does not read legacy state:
initialize a fresh state root during the hard-cut upgrade, then dry-run and
reverify the existing destination before enabling scheduled runs.
