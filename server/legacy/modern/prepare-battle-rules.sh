#!/bin/sh
# Archive only combat tables, never setup.cf (which contains credentials).
# Called from the GMSV working directory before loading the same tables.
set -eu
umask 077
config="${1:?configuration required}"
mode="${2:-archive}"
case "$mode" in
    archive)
        root="${STONEAGE_BATTLE_RECORD_DIR:?record directory required}"
        mkdir -p "$root/rulesets"
        stage="$(mktemp -d "$root/rulesets/.prepare.XXXXXX")" ;;
    --digest-only)
        stage="$(mktemp -d "${TMPDIR:-/tmp}/stoneage-rules.XXXXXX")" ;;
    *) echo "Unsupported battle rules mode" >&2; exit 2 ;;
esac
trap 'rm -rf "$stage"' EXIT INT TERM
digest() {
    sum="$(sha256sum "$1")" || return 1
    printf '%s\n' "${sum%% *}"
}
binary="${STONEAGE_BATTLE_BINARY:-./gmsvjt.exe}"
binary_digest="$(digest "$binary")"
printf 'format stoneage-native-rules-v2\nbinary %s\n' "$binary_digest" > "$stage/SHA256SUMS"
# The native CLI emits only selected table paths and numeric gameplay options.
# Its diagnostics go to stderr. Paths are used only inside this private stage,
# never archived; neither setup.cf nor host/account configuration is copied.
"$binary" --battle-rules --config "$config" > "$stage/inputs.tsv"
: > "$stage/combat-options.txt"
while IFS="$(printf '\t')" read -r kind key value; do
    case "$key" in ''|*[!a-z0-9_-]*) echo 'Invalid native rule key' >&2; exit 1 ;; esac
    case "$kind" in
        table|optional)
            if [ "$kind" = optional ] && [ ! -e "$value" ]; then
                printf '%s absent\n' "$key" >> "$stage/SHA256SUMS"
                continue
            fi
            [ -f "$value" ] && [ -r "$value" ] || { echo "Battle recorder: missing rule table $key" >&2; exit 1; }
            cp "$value" "$stage/$key"
            table_digest="$(digest "$stage/$key")"
            printf '%s %s\n' "$key" "$table_digest" >> "$stage/SHA256SUMS" ;;
        option)
            case "$value" in ''|*[!0-9-]*) echo 'Invalid native numeric option' >&2; exit 1 ;; esac
            printf '%s=%s\n' "$key" "$value" >> "$stage/combat-options.txt" ;;
        *) echo 'Invalid native rule input' >&2; exit 1 ;;
    esac
done < "$stage/inputs.tsv"
rm "$stage/inputs.tsv"
printf 'combat-options %s\n' "$(digest "$stage/combat-options.txt")" >> "$stage/SHA256SUMS"
ruleset="$(digest "$stage/SHA256SUMS")"
if [ "$mode" = archive ]; then
    destination="$root/rulesets/$ruleset"
    # mv -T prevents concurrent workers from nesting their staging directory
    # inside an archive published by another worker. Verify the winner instead.
    if [ ! -e "$destination" ] && [ ! -L "$destination" ] && mv -T "$stage" "$destination" 2>/dev/null; then
        :
    else
        [ -d "$destination" ] && [ ! -L "$destination" ] || { echo 'Invalid existing rule archive' >&2; exit 1; }
        for source in "$stage"/*; do
            archived="$destination/${source##*/}"
            [ -f "$archived" ] && [ ! -L "$archived" ] || { echo 'Incomplete existing rule archive' >&2; exit 1; }
            expected="$(digest "$source")"
            actual="$(digest "$archived")"
            [ "$expected" = "$actual" ] || { echo 'Corrupt existing rule archive' >&2; exit 1; }
        done
    fi
fi
printf '%s\n' "$ruleset"
