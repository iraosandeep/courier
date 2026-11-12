#!/bin/sh
# Courier installer
#
#   curl -fsSL https://raw.githubusercontent.com/iraosandeep/courier/main/install.sh | sh
#
# Re-run at any time to upgrade.
#
# Options (environment variables):
#   COURIER_VERSION=v0.2.0       install a specific version (default: latest release)
#   COURIER_INSTALL_DIR=~/bin    install directory (default: ~/.local/bin)
#   COURIER_NO_MODIFY_PATH=1     don't add the install directory to your shell profile
#
# Uninstall:
#   curl -fsSL https://raw.githubusercontent.com/iraosandeep/courier/main/install.sh | sh -s -- --uninstall

set -eu

REPO="iraosandeep/courier"
BASE_URL="${COURIER_DOWNLOAD_BASE:-https://github.com/$REPO/releases}"
INSTALL_DIR="${COURIER_INSTALL_DIR:-$HOME/.local/bin}"
BIN="$INSTALL_DIR/courier"

say() { printf '%s\n' "$*"; }
err() {
	printf 'courier-install: error: %s\n' "$*" >&2
	exit 1
}
has() { command -v "$1" >/dev/null 2>&1; }

fetch() { # fetch URL DEST
	if has curl; then
		curl -fsSL --retry 3 --proto '=https,http' -o "$2" "$1"
	elif has wget; then
		wget -q -O "$2" "$1"
	else
		err "curl or wget is required"
	fi
}

latest_version() {
	if has curl; then
		# github.com/<repo>/releases/latest redirects to .../releases/tag/<tag>;
		# following it avoids the GitHub API rate limit.
		url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$BASE_URL/latest") || return 1
		v=${url##*/}
	else
		v=$(wget -qO- "https://api.github.com/repos/$REPO/releases/latest" |
			sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
	fi
	case "$v" in
	v[0-9]*) printf '%s' "$v" ;;
	*) return 1 ;;
	esac
}

sha256() {
	if has sha256sum; then
		sha256sum "$1" | cut -d ' ' -f 1
	elif has shasum; then
		shasum -a 256 "$1" | cut -d ' ' -f 1
	else
		err "sha256sum or shasum is required to verify the download"
	fi
}

detect_platform() {
	case "$(uname -s)" in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	MINGW* | MSYS* | CYGWIN*)
		err "on Windows, download courier_<version>_windows_amd64.zip from https://github.com/$REPO/releases"
		;;
	*) err "unsupported operating system: $(uname -s)" ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) err "unsupported architecture: $(uname -m)" ;;
	esac
	# A shell running under Rosetta on Apple silicon reports x86_64; prefer native.
	if [ "$os" = darwin ] && [ "$arch" = amd64 ] &&
		[ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
		arch=arm64
	fi
}

ensure_path() {
	case ":$PATH:" in
	*":$INSTALL_DIR:"*) return 0 ;;
	esac

	case "$(basename "${SHELL:-sh}")" in
	zsh)
		rc="${ZDOTDIR:-$HOME}/.zshrc"
		line="export PATH=\"$INSTALL_DIR:\$PATH\""
		;;
	bash)
		if [ "$(uname -s)" = Darwin ]; then rc="$HOME/.bash_profile"; else rc="$HOME/.bashrc"; fi
		line="export PATH=\"$INSTALL_DIR:\$PATH\""
		;;
	fish)
		rc="$HOME/.config/fish/config.fish"
		line="fish_add_path \"$INSTALL_DIR\""
		;;
	*)
		rc="$HOME/.profile"
		line="export PATH=\"$INSTALL_DIR:\$PATH\""
		;;
	esac

	if [ "${COURIER_NO_MODIFY_PATH:-}" = 1 ]; then
		say ""
		say "$INSTALL_DIR is not on your PATH. Add it with:"
		say "  $line"
		return 0
	fi

	if ! { [ -f "$rc" ] && grep -Fq "$INSTALL_DIR" "$rc"; }; then
		mkdir -p "$(dirname "$rc")"
		printf '\n# Added by the Courier installer\n%s\n' "$line" >>"$rc"
		say "Added $INSTALL_DIR to PATH in $rc"
	fi
	say ""
	say "Open a new terminal, or run this to use courier now:"
	say "  $line"
}

uninstall() {
	if [ -e "$BIN" ]; then
		rm -f "$BIN"
		say "Removed $BIN"
	else
		say "courier is not installed in $INSTALL_DIR"
	fi
	say "Your configuration was kept. To remove it too: rm -rf \"\${XDG_CONFIG_HOME:-\$HOME/.config}/courier\""
}

main() {
	case "${1:-}" in
	--uninstall)
		uninstall
		return 0
		;;
	"") ;;
	*) err "unknown option: $1 (supported: --uninstall)" ;;
	esac

	has tar || err "tar is required"
	detect_platform

	version="${COURIER_VERSION:-}"
	if [ -z "$version" ]; then
		version=$(latest_version) ||
			err "could not find the latest release at https://github.com/$REPO/releases (none published yet?). Set COURIER_VERSION=vX.Y.Z to choose one."
	fi
	case "$version" in
	v*) ;;
	*) version="v$version" ;;
	esac

	archive="courier_${version#v}_${os}_${arch}.tar.gz"
	tmp=$(mktemp -d 2>/dev/null || mktemp -d -t courier)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	if [ -x "$BIN" ]; then
		say "Upgrading $("$BIN" version 2>/dev/null | cut -d ' ' -f 1-2 || echo courier) to $version ($os/$arch)..."
	else
		say "Installing courier $version ($os/$arch)..."
	fi

	fetch "$BASE_URL/download/$version/$archive" "$tmp/$archive" ||
		err "download failed: $BASE_URL/download/$version/$archive"
	fetch "$BASE_URL/download/$version/checksums.txt" "$tmp/checksums.txt" ||
		err "could not download checksums for $version"

	expected=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
	[ -n "$expected" ] || err "no checksum listed for $archive"
	actual=$(sha256 "$tmp/$archive")
	[ "$expected" = "$actual" ] || err "checksum mismatch for $archive (expected $expected, got $actual)"

	tar -xzf "$tmp/$archive" -C "$tmp" courier || err "could not extract $archive"

	mkdir -p "$INSTALL_DIR" 2>/dev/null || err "cannot create $INSTALL_DIR; set COURIER_INSTALL_DIR to a writable directory"
	[ -w "$INSTALL_DIR" ] || err "$INSTALL_DIR is not writable; set COURIER_INSTALL_DIR or re-run with sudo"

	# Copy then rename so a running courier is never left half-written.
	cp "$tmp/courier" "$INSTALL_DIR/.courier.new.$$"
	chmod 0755 "$INSTALL_DIR/.courier.new.$$"
	mv -f "$INSTALL_DIR/.courier.new.$$" "$BIN"

	say "Installed $("$BIN" version) to $BIN"

	other=$(command -v courier 2>/dev/null || true)
	if [ -n "$other" ] && [ "$other" != "$BIN" ]; then
		say ""
		say "warning: another courier at $other comes first on your PATH; remove it or reorder PATH."
	fi

	ensure_path

	say ""
	say "Get started:"
	say "  courier setup    # connect your Telegram bot"
	say "  courier          # start the bot"
}

main "$@"
