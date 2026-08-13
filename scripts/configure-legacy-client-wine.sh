#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "$0")/.." && pwd)"
wine_bin="${STONEAGE_WINE:-$(command -v wine || true)}"
wine_prefix="${1:-${STONEAGE_WINEPREFIX:-$project_root/runtime/wine-prefix-zhcn}}"
wine_locale="${STONEAGE_WINE_LOCALE:-zh_CN.GBK}"
font_override="${STONEAGE_CJK_FONT:-}"
font_marker="$wine_prefix/.stoneage-cjk-font"
font_config_version="2"

if [[ -z "$wine_bin" || ! -x "$wine_bin" ]]; then
  echo "Wine is not installed or STONEAGE_WINE is not executable." >&2
  exit 1
fi

mkdir -p "$wine_prefix"
export WINEPREFIX="$wine_prefix"
export LANG="$wine_locale"
export LC_ALL="$wine_locale"
export WINEDEBUG="${WINEDEBUG:--all}"
export MVK_CONFIG_LOG_LEVEL="${MVK_CONFIG_LOG_LEVEL:-0}"

if [[ ! -f "$wine_prefix/system.reg" ]]; then
  "$wine_bin" wineboot -u
fi

wine_acp="$("$wine_bin" reg query 'HKLM\System\CurrentControlSet\Control\Nls\CodePage' /v ACP 2>/dev/null | tr -d '\r' | awk '$1 == "ACP" { print $3 }')"
if [[ "$wine_acp" != "936" ]]; then
  echo "Wine prefix $wine_prefix uses ANSI code page ${wine_acp:-unknown}, not CP936." >&2
  echo "Create a fresh prefix under zh_CN.GBK; do not reuse an en-US prefix." >&2
  exit 1
fi

font_source=""
font_family=""
if [[ -n "$font_override" ]]; then
  font_source="$font_override"
elif [[ -f "$project_root/vendor/assets/fonts/NotoSansCJKsc-Regular.otf" ]]; then
  font_source="$project_root/vendor/assets/fonts/NotoSansCJKsc-Regular.otf"
  font_family="Noto Sans CJK SC"
elif [[ -f /System/Library/Fonts/Supplemental/Arial\ Unicode.ttf ]]; then
  font_source="/System/Library/Fonts/Supplemental/Arial Unicode.ttf"
  font_family="Arial Unicode MS"
elif [[ -f /System/Library/Fonts/Hiragino\ Sans\ GB.ttc ]]; then
  font_source="/System/Library/Fonts/Hiragino Sans GB.ttc"
  font_family="Hiragino Sans GB"
fi

if [[ ! -f "$font_source" ]]; then
  echo "No Simplified Chinese font was found." >&2
  echo "Set STONEAGE_CJK_FONT to a CJK .ttf, .ttc, or .otf file." >&2
  exit 1
fi

if [[ -z "$font_family" ]] && command -v fc-scan >/dev/null 2>&1; then
  font_family="$(fc-scan --format '%{family[0]}' "$font_source" 2>/dev/null | head -n 1)"
fi
font_family="${font_family:-Arial Unicode MS}"

font_ext="$(printf '%s' "${font_source##*.}" | tr '[:upper:]' '[:lower:]')"
font_name="stoneage-cjk.$font_ext"
font_dir="$wine_prefix/drive_c/windows/Fonts"

if [[ -f "$font_marker" ]] &&
   grep -qx "version=$font_config_version" "$font_marker" &&
   [[ -f "$font_dir/$font_name" ]]; then
  exit 0
fi

mkdir -p "$font_dir"
install -m 0644 "$font_source" "$font_dir/$font_name"

fonts_key='HKLM\Software\Microsoft\Windows NT\CurrentVersion\Fonts'
substitutes_key='HKLM\Software\Microsoft\Windows NT\CurrentVersion\FontSubstitutes'
wine_replacements_key='HKCU\Software\Wine\Fonts\Replacements'
"$wine_bin" reg add "$fonts_key" /v "$font_family (OpenType)" /t REG_SZ /d "$font_name" /f >/dev/null
for legacy_family in \
  SimSun NSimSun '宋体' '新宋体' \
  'MS Shell Dlg' 'MS Shell Dlg 2' 'MS Sans Serif' 'Microsoft Sans Serif' \
  Tahoma Arial; do
  "$wine_bin" reg add "$substitutes_key" /v "$legacy_family" /t REG_SZ /d "$font_family" /f >/dev/null
  "$wine_bin" reg add "$wine_replacements_key" /v "$legacy_family" /t REG_SZ /d "$font_family" /f >/dev/null
done

# The ACP query and registry updates above start wineserver before the new font
# is installed. Wine builds their GDI font cache once per server lifetime, so a
# client launched immediately afterwards would otherwise keep rendering CJK as
# missing-glyph boxes. This prefix is dedicated to StoneAge; restart only this
# prefix's wineserver before marking the configuration complete.
wineserver_bin="${STONEAGE_WINESERVER:-$(command -v wineserver || true)}"
if [[ -n "$wineserver_bin" && -x "$wineserver_bin" ]]; then
  "$wineserver_bin" -k
  "$wineserver_bin" -w
fi

printf 'version=%s\nsource=%s\nfamily=%s\nfile=%s\n' \
  "$font_config_version" "$font_source" "$font_family" "$font_name" >"$font_marker"
echo "Configured Wine CP936 font fallback: $font_family"
