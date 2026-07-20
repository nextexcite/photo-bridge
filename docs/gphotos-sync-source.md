# Community Google Photos source lane

Google Photos browser automation is intentionally not bundled into the primary
`photo-bridge` image.

The v0.2 source lane reuses unmodified
[`spraot/gphotos-sync`](https://github.com/spraot/gphotos-sync) at commit
[`f07f6839a9b73cb78c00bcd58d6e6c4ee3474879`](https://github.com/spraot/gphotos-sync/commit/f07f6839a9b73cb78c00bcd58d6e6c4ee3474879).

## Runtime contract

- Build the exact upstream commit; do not fork it into this repository.
- Run upstream `no-cron` mode. An external systemd timer owns scheduling.
- Mount its authenticated browser profile as private, persistent state.
- Mount a separate download landing directory.
- Run a normal filesystem-source `photo-bridge` job only after the downloader
  exits successfully.
- Do not inject an account password into environment variables. Refresh the
  browser profile interactively when required.
- Keep account identifiers, album IDs, language settings, healthcheck IDs, and
  run evidence in the private operations control plane.

## Risk boundary

The upstream image currently installs amd64 Google Chrome and recommends a
privileged Chromium container. Use an isolated x86-64 experiment host with no
inbound service and no access to unrelated credentials. A browser-driven sync
can break whenever the Google Photos website changes, so it is supplementary,
not the only archive.

Google Takeout remains the authoritative baseline for original media and album
metadata.
