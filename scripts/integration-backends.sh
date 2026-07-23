#!/usr/bin/env bash
set -euo pipefail

# Credential-free integration for public CI. The values below exist only inside
# disposable CI services and must never be repurposed for a live backend.
repo_root=$(git rev-parse --show-toplevel)
work_root=$(mktemp -d)
trap 'rm -rf "$work_root"' EXIT

: "${MINIO_ENDPOINT:=http://127.0.0.1:9000}"
: "${MINIO_ACCESS_KEY:=minioadmin}"
: "${MINIO_SECRET_KEY:=minioadmin}"
: "${WEBDAV_ENDPOINT:=http://127.0.0.1:8080}"
: "${WEBDAV_USER:=fixture-user}"
: "${WEBDAV_PASSWORD:=fixture-password}"

binary="$work_root/photo-bridge"
config="$work_root/config.yaml"
state="$work_root/state"
rclone_config="$work_root/rclone.conf"
source="$work_root/source"
webdav_root="$work_root/webdav-root"
mkdir -p "$source/nested" "$state"
mkdir -p "$webdav_root"
printf 'backend alpha\n' > "$source/alpha.txt"
printf 'backend nested\n' > "$source/nested/item.txt"

cat > "$rclone_config" <<EOF
[fixture-minio]
type = s3
provider = Minio
access_key_id = ${MINIO_ACCESS_KEY}
secret_access_key = ${MINIO_SECRET_KEY}
endpoint = ${MINIO_ENDPOINT}
region = us-east-1
no_check_bucket = true

[fixture-webdav]
type = webdav
url = ${WEBDAV_ENDPOINT}
vendor = other
user = ${WEBDAV_USER}
pass = ${WEBDAV_PASSWORD}
EOF

export PHOTOBRIDGE_RCLONE_CONFIG_FILE="$rclone_config"
rclone serve webdav "$webdav_root" --addr 127.0.0.1:8080 --user "$WEBDAV_USER" --pass "$WEBDAV_PASSWORD" >"$work_root/webdav.log" 2>&1 &
webdav_pid=$!
trap 'kill "$webdav_pid" 2>/dev/null || true; rm -rf "$work_root"' EXIT
for attempt in {1..20}; do
  curl --fail --silent --output /dev/null --user "$WEBDAV_USER:$WEBDAV_PASSWORD" "$WEBDAV_ENDPOINT/" && break
  sleep 1
done
kill -0 "$webdav_pid"
rclone --config "$rclone_config" mkdir fixture-minio:photo-bridge-source
rclone --config "$rclone_config" copy "$source" fixture-minio:photo-bridge-source

cat > "$config" <<'EOF'
apiVersion: photo-bridge/v1alpha2
jobs:
  - name: backend-fixture
    operation: copy
    source:
      driver: rclone
      remote: fixture-minio
      path: photo-bridge-source
    destination:
      driver: rclone
      remote: fixture-webdav
      path: photo-bridge-destination
    policy:
      integrity:
        manifest: auto
        verification: auto
        allowEmpty: false
      transfer:
        transfers: 2
        checkers: 2
        bufferSize: 4MiB
        maxBufferMemory: 16MiB
      limits:
        maxDuration: 0s
        maxFiles: 0
        maxBytes: 0
      progressInterval: 1s
      retention:
        mode: automatic
        maxAge: 720h
        minRuns: 5
EOF

go build -trimpath -o "$binary" "$repo_root/cmd/photo-bridge"
"$binary" config validate --config "$config"
"$binary" run --config "$config" --state-dir "$state" --job backend-fixture > "$work_root/report.json"
rg -q '"status"\s*:\s*"succeeded"' "$work_root/report.json"
rclone --config "$rclone_config" cat fixture-webdav:photo-bridge-destination/alpha.txt | cmp "$source/alpha.txt" -
rclone --config "$rclone_config" cat fixture-webdav:photo-bridge-destination/nested/item.txt | cmp "$source/nested/item.txt" -

# An unrelated destination object is a persistent non-destructive invariant.
printf 'destination-only\n' | rclone --config "$rclone_config" rcat fixture-webdav:photo-bridge-destination/destination-only.txt
"$binary" run --config "$config" --state-dir "$state" --job backend-fixture > "$work_root/rerun.json"
rclone --config "$rclone_config" cat fixture-webdav:photo-bridge-destination/destination-only.txt | rg -q '^destination-only$'

echo "backend integration passed"
