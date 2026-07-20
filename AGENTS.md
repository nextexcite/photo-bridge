# photo-bridge Agent Instructions

`photo-bridge` is a public Go project. Treat every tracked byte and every Git
commit header as public information.

## Public-safety rules

- Never commit or stage real account names, email addresses, album IDs,
  hostnames, domains, IP addresses, bucket names, remote names, filesystem home
  paths, OAuth identifiers, tokens, cookies, browser profiles, media, Takeout
  archives, runtime state, or live run reports.
- Use only `example.invalid`, RFC-reserved documentation addresses, generic
  endpoint names, and synthetic fixtures in tracked files and tests.
- Keep personal server inventory, live job configuration, OAuth material,
  runtime reports, and provider canary evidence in the private operations
  control plane, never in this repository.
- Do not print secret values while validating configuration or debugging. Logs
  and reports must remain redacted at normal verbosity.
- Do not push any branch or tag until the operator explicitly authorizes the
  first public push. Run `just public-audit` immediately before every commit
  and push.
- Git author and committer email addresses must use a GitHub-provided
  `users.noreply.github.com` address. Do not add personal co-author trailers.

## Runtime and build rules

- Go is the application and orchestration language. Shell is limited to narrow
  development and audit glue.
- Rclone owns storage protocols. Do not add provider-specific transfer clients
  when an rclone backend already provides the capability.
- Copy operations are non-destructive. Do not introduce `sync`, `move`,
  destination deletion, or source deletion without an explicit contract change.
- OCI images are built by GitHub Actions. Never build project images on a local
  macOS workstation.
- Annotated `vX.Y.Z` Git tags are the release authority. Operators use exact
  SemVer image tags; digests are integrity evidence and rollback coordinates,
  not the normal human-facing command surface.
- Runtime image experiments run only on the operator-designated private Linux
  experiment host through the private operations control plane. Do not record
  that host's identity or coordinates here.
- The application image must remain rootless, one-shot, without FUSE, a Docker
  socket, an embedded scheduler, telemetry, or inbound listeners.

## Verification

Run the narrow checks first:

```text
just fmt-check
just test
just vet
just public-audit
```

Do not treat cloud or live-provider canaries as public CI. They belong in the
private operations repository.
