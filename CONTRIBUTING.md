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

`just public-audit` runs gitleaks, checks the outgoing Git history, rejects
personal home-path shapes and non-example email addresses, and refuses tracked
runtime/media artifacts. Do not bypass the gate to make a change pass.

## Design boundaries

- Keep Go at the center of orchestration and state handling.
- Let rclone own provider protocols.
- Preserve copy-only, non-destructive semantics.
- Keep schedulers and secret managers outside the image.
- Keep community Google Photos browser automation in its own container.

## Reporting failures

Redact account names, remotes, paths, host identities, URLs, and media names
before sharing diagnostics. Prefer the structured status and exit code over a
raw debug log.
