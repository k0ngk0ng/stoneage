#!/bin/sh
# Archive only combat tables, never setup.cf (which contains credentials).
# Called from the GMSV working directory before loading the same tables.
set -eu
umask 077
root="${STONEAGE_BATTLE_RECORD_DIR:?record directory required}"
config="${1:?configuration required}"
mkdir -p "$root/rulesets"
stage="$(mktemp -d "$root/rulesets/.prepare.XXXXXX")"
trap 'rm -rf "$stage"' EXIT INT TERM
digest() { sha256sum "$1" | awk '{print $1}'; }
printf 'binary %s\n' "$(digest ./gmsvjt.exe)" > "$stage/SHA256SUMS"
for key in itemset6file itemset5file itemset4file itemset3file itemfile magicfile attmagicfile petskillfile2 petskillfile1 enemyfile enemybasefile; do
    path="$(awk -F= -v key="$key" '$1 == key {sub(/\r$/, "", $2); print $2; exit}' "$config")"
    [ -n "$path" ] || continue
    [ -f "$path" ] || { echo "Battle recorder: missing rule table $key" >&2; exit 1; }
    cp "$path" "$stage/$key"
    printf '%s %s\n' "$key" "$(digest "$stage/$key")" >> "$stage/SHA256SUMS"
done
# Only explicit numeric gameplay options; never archive arbitrary config lines.
awk -F= '$1 ~ /^(skup|battleexp|battlegold|battledamage|battledebugmsg|enemyspeed|ridetype|ridemode)$/ && $2 ~ /^[0-9]+\r?$/ {sub(/\r$/, ""); print}' "$config" > "$stage/combat-options.txt"
printf 'combat-options %s\n' "$(digest "$stage/combat-options.txt")" >> "$stage/SHA256SUMS"
ruleset="$(digest "$stage/SHA256SUMS")"
if [ ! -d "$root/rulesets/$ruleset" ]; then
    mv "$stage" "$root/rulesets/$ruleset"
fi
printf '%s\n' "$ruleset"
