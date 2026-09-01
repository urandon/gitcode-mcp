#!/bin/sh
set -eu

fail() {
	echo "product docs check: $*" >&2
	exit 1
}

for contract in \
	"README.md|## How It Works" \
	"README.md|## Product Surfaces" \
	"README.md|## Quick Start" \
	"README.md|## Reliability By Design" \
	"AGENTS.md|## Autonomous Delivery Protocol" \
	"AGENTS.md|## Stop Conditions" \
	"AGENTS.md|## Delivery Checklists"
do
	file=${contract%%|*}
	text=${contract#*|}
	grep -F "$text" "$file" >/dev/null || fail "$file is missing required section: $text"
done

for surface in CLI MCP "Admin UI"
do
	grep -F "$surface" README.md >/dev/null || fail "README.md is missing product surface: $surface"
done

mermaid_blocks=$(awk '
	/^```mermaid$/ { if (open) exit 2; open=1; count++; next }
	/^```$/ && open { open=0 }
	END { if (open) exit 3; print count+0 }
' README.md) || fail "README.md has an unbalanced Mermaid fence"
[ "$mermaid_blocks" -eq 1 ] || fail "README.md must contain exactly one architecture Mermaid block"

awk '
	/^```mermaid$/ { open=1; next }
	/^```$/ && open { open=0 }
	open && /^flowchart / { flowchart=1 }
	END { exit flowchart ? 0 : 1 }
' README.md || fail "README.md Mermaid block must declare a flowchart"

awk '
	{
		line=$0
		while (match(line, /\]\([^)]*\)/)) {
			print substr(line, RSTART+2, RLENGTH-3)
			line=substr(line, RSTART+RLENGTH)
		}
	}
' README.md AGENTS.md | sort -u | while IFS= read -r link
do
	case "$link" in
		http://*|https://*|mailto:*|'#'*) continue ;;
	esac
	path=${link%%#*}
	[ -n "$path" ] || continue
	[ -e "$path" ] || fail "missing repository-relative link target: $link"
done

echo "product docs contracts are valid"
