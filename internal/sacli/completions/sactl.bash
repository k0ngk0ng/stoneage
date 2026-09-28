# Bash 3.2+; completion never logs in or sends game commands.
_sactl_complete() {
    local result item current
    current="${COMP_WORDS[COMP_CWORD]}"
    COMPREPLY=()
    result="$("${COMP_WORDS[0]}" __complete "${COMP_WORDS[@]:1:COMP_CWORD}" 2>/dev/null)" || return 0
    case "$result" in
        @files|@dirs)
            local kind=file
            [[ "$result" != @dirs ]] || kind=directory
            while IFS= read -r item; do COMPREPLY+=("$item"); done < <(compgen -A "$kind" -- "$current")
            type compopt &>/dev/null && compopt -o filenames
            ;;
        *)
            while IFS= read -r item; do
                [[ -z "$item" ]] || COMPREPLY+=("$item")
            done <<< "$result"
            ;;
    esac
    return 0
}
complete -o filenames -F _sactl_complete sactl
