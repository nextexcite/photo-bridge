#!/usr/bin/env bash
set -euo pipefail

archive=rclone-v1.74.4-linux-amd64.zip
curl --fail --location --silent --show-error \
  "https://downloads.rclone.org/v1.74.4/${archive}" --output "/tmp/${archive}"
echo "fe435e0c36228e7c2f116a8701f01127bb1f694005fc11d1f27186c8bca4115d  /tmp/${archive}" | sha256sum --check --strict
unzip -q "/tmp/${archive}" -d /tmp/rclone
sudo install -m 0755 /tmp/rclone/rclone-v1.74.4-linux-amd64/rclone /usr/local/bin/rclone
