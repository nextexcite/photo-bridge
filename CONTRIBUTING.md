# Contributing

Contributions are welcome, but this repository is public and intentionally
contains no live account, storage, or server configuration.

## Before opening a change

1. Use synthetic fixtures and `example.invalid` identifiers only.
2. Do not attach media, Takeout archives, browser profiles, rclone configs,
   cloud logs, or live run reports to commits or issues.
3. Configure Git with a public or GitHub-provided noreply author email.
4. Run:

   ```text
   mise install
   just check
   just integration
   ```

5. Review every staged file and commit header before pushing.

`just public-audit` runs gitleaks against the worktree and complete Git
history, checks outgoing commit identities, rejects non-example emails,
unapproved domains and IPs, sensitive path shapes, binary/media archives, and
credential-like material. Its adversarial fixtures must pass; do not bypass the
gate to make a change pass.

## Design boundaries

- Keep Go at the center of orchestration and state handling.
- Let rclone own provider protocols.
- Preserve copy-only, non-destructive semantics.
- Keep schedulers and secret managers outside the image.
- Keep community Google Photos browser automation in its own container.

## Reporting failures

Redact account names, remotes, paths, host identities, URLs, and media names
before sharing diagnostics. Prefer JSON stdout and sanitized stderr progress
over a raw debug log.
