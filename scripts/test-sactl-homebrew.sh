#!/usr/bin/env bash
# Run only in a disposable CI runner: installs the staged Formula with Homebrew.
set -euo pipefail
test "${GITHUB_ACTIONS:-}" = true
export HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_ANALYTICS=1
export HOMEBREW_CACHE="$PWD/.cache/homebrew" HOMEBREW_LOGS="$PWD/.cache/homebrew-logs" HOMEBREW_TEMP="$PWD/.cache/tmp"
mkdir -p "$HOMEBREW_CACHE" "$HOMEBREW_LOGS" "$HOMEBREW_TEMP"
brew tap k0ngk0ng/stoneage "${GITHUB_SERVER_URL}/${GITHUB_REPOSITORY}"
tap="$(brew --repository k0ngk0ng/stoneage)"
cp Formula/sactl.rb "$tap/Formula/sactl.rb"
brew install k0ngk0ng/stoneage/sactl
brew test k0ngk0ng/stoneage/sactl
prefix="$(brew --prefix k0ngk0ng/stoneage/sactl)"
version="$(python3 -c 'import json; print(json.load(open("packaging/sactl-catalog/release-lock.json"))["version"])')"
actual="$("$prefix/bin/sactl" version)"
[[ "$actual" == *"$version"* ]]
brew reinstall k0ngk0ng/stoneage/sactl
brew test k0ngk0ng/stoneage/sactl
brew uninstall k0ngk0ng/stoneage/sactl
