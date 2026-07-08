#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -eu

usage() {
	echo "Usage: $0 KEY VALUE FILE [FILE ...]" >&2
	exit 2
}

[ "$#" -ge 3 ] || usage

key=$1
value=$2
shift 2

update_file() {
	file=$1
	dir=$(dirname "$file")
	tmp=$(mktemp "$dir/.${file##*/}.XXXXXX")
	trap 'rm -f "$tmp"' INT TERM HUP EXIT

	if [ -f "$file" ] && grep -q "^${key}=" "$file"; then
		awk -v key="$key" -v value="$value" '
			BEGIN { updated = 0 }
			$0 ~ "^" key "=" && !updated {
				print key "='\''" value "'\''"
				updated = 1
				next
			}
			{ print }
			END {
				if (!updated) {
					print key "='\''" value "'\''"
				}
			}
		' "$file" > "$tmp"
		mv "$tmp" "$file"
		echo "Updated $key in $file"
	elif [ -f "$file" ]; then
		printf "%s='%s'\n" "$key" "$value" >> "$file"
		echo "Appended $key to $file"
	else
		printf "%s='%s'\n" "$key" "$value" > "$file"
		echo "Created $file with $key"
	fi

	trap - INT TERM HUP EXIT
	rm -f "$tmp"
}

for file in "$@"; do
	update_file "$file"
done
