#!/bin/sh
set -eu

# Browser CI is a semantic contract gate. Explicit screenshots, Playwright
# traces, and videos belong to opt-in local QA and must never become CI output.
roots="web/test-results web/playwright-report"
found=""
for root in $roots; do
	if [ ! -d "$root" ]; then
		continue
	fi
	found=$(find "$root" -type f \( \
		-name '*.png' -o -name '*.jpg' -o -name '*.jpeg' -o \
		-name '*.webp' -o -name '*.gif' -o -name '*.webm' -o \
		-name '*.mp4' -o -name 'trace.zip' \
	\) -print)
	if [ -n "$found" ]; then
		break
	fi
done

if [ -n "$found" ]; then
	echo "browser CI produced forbidden visual artifacts:" >&2
	echo "$found" >&2
	exit 1
fi

echo "browser CI artifacts are semantic-only"
