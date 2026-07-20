# Security Policy

## Supported versions

Until the first stable release, only the latest tagged version is supported.

## Reporting a vulnerability

Use GitHub private vulnerability reporting for security-sensitive reports. Do
not open a public issue containing credentials, personal infrastructure data,
browser profiles, media, or unredacted logs.

## Credential boundary

`photo-bridge` consumes only a path to a runtime-mounted rclone configuration.
It does not fetch broad provider catalogs and does not store credential values
in job YAML or reports. Public CI has no provider credentials.

The normal log level is fixed at `INFO`, and logs pass through redaction before
being written or displayed. Treat runtime state as private even after
redaction; it can still reveal filenames and operational timing.
