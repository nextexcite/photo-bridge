#!/usr/bin/env bash
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
work_root=$(mktemp -d)
trap 'rm -rf "$work_root"' EXIT

source_dir="$work_root/source"
destination_dir="$work_root/destination"
state_dir="$work_root/state"
config_file="$work_root/config.yaml"
binary="$work_root/photo-bridge"

mkdir -p "$source_dir/nested" "$destination_dir" "$state_dir"
printf 'alpha\n' > "$source_dir/alpha.txt"
printf 'nested\n' > "$source_dir/nested/item.txt"

sed \
  -e '/^  # Google Takeout/,$d' \
  -e "s|/data/input|$source_dir|" \
  -e 's|driver: rclone|driver: filesystem|' \
  -e '/remote: archive-remote/d' \
  -e "s|path: photos/account-a|path: $destination_dir|" \
  "$repo_root/config.example.yaml" > "$config_file"

go build -trimpath -o "$binary" "$repo_root/cmd/photo-bridge"

"$binary" run --config "$config_file" --state-dir "$state_dir" --job example-archive >/dev/null
cmp "$source_dir/alpha.txt" "$destination_dir/alpha.txt"
cmp "$source_dir/nested/item.txt" "$destination_dir/nested/item.txt"

printf 'destination-only\n' > "$destination_dir/destination-only.txt"
"$binary" run --config "$config_file" --state-dir "$state_dir" --job example-archive >/dev/null
test -f "$destination_dir/destination-only.txt"

printf 'changed\n' > "$source_dir/alpha.txt"
printf 'new\n' > "$source_dir/new.txt"
"$binary" run --config "$config_file" --state-dir "$state_dir" --job example-archive >/dev/null
cmp "$source_dir/alpha.txt" "$destination_dir/alpha.txt"
cmp "$source_dir/new.txt" "$destination_dir/new.txt"
test -f "$destination_dir/destination-only.txt"

"$binary" verify --config "$config_file" --state-dir "$state_dir" --job example-archive >/dev/null
"$binary" status --state-dir "$state_dir" --job example-archive --json | rg -q '"status": "succeeded"'

takeout_source="$work_root/takeout-source"
takeout_destination="$work_root/takeout-destination"
takeout_state="$work_root/takeout-state"
takeout_config="$work_root/takeout-config.yaml"
mkdir -p "$takeout_source" "$takeout_destination" "$takeout_state"
printf 'older\n' > "$takeout_source/takeout-20260719T010000Z-001.zip"
printf 'part-one\n' > "$takeout_source/takeout-20260720T010000Z-001.zip"
printf 'part-two\n' > "$takeout_source/takeout-20260720T010000Z-002.zip"
printf 'not-an-archive\n' > "$takeout_source/notes.txt"

sed \
  -e '/^  - name: example-archive/,/^  # Google Takeout/{ /^  # Google Takeout/!d; }' \
  -e 's/driver: rclone/driver: filesystem/' \
  -e '/remote: source-drive/d' \
  -e "s|path: Takeout|path: $takeout_source|" \
  -e "s|path: /data/takeout-archive|path: $takeout_destination|" \
  "$repo_root/config.example.yaml" > "$takeout_config"

"$binary" run --config "$takeout_config" --state-dir "$takeout_state" --job example-takeout --takeout-ready >/dev/null
test -f "$takeout_destination/takeout-20260720T010000Z-001.zip"
test -f "$takeout_destination/takeout-20260720T010000Z-002.zip"
test ! -e "$takeout_destination/takeout-20260719T010000Z-001.zip"
test ! -e "$takeout_destination/notes.txt"

set +e
"$binary" run --config "$takeout_config" --state-dir "$takeout_state" --job example-takeout >/dev/null 2>&1
takeout_poll_status=$?
set -e
test "$takeout_poll_status" -eq 6

echo "local integration passed"
