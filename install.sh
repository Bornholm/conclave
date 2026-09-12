#!/bin/sh
# Installs or updates conclave from the GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/bornholm/conclave/main/install.sh | sh
#
# On Debian/Ubuntu the .deb is installed with apt or dpkg, on Arch/Manjaro the
# pacman package. Anywhere else (macOS, other distributions, no root) the
# binary is extracted from the release archive into a prefix.
#
# Options / environment variables:
#   --version <tag>   CONCLAVE_VERSION     release tag, e.g. v0.2.0 (default: latest)
#   --binary          CONCLAVE_BINARY=1    skip the system package, install the bare binary
#   --prefix <dir>    CONCLAVE_PREFIX      where the bare binary goes (default: /usr/local/bin,
#                                          or ~/.local/bin when not root and without sudo)
#   --force           CONCLAVE_FORCE=1     reinstall even if this version is already installed
#   --download-only                        download and verify without installing
#                     CONCLAVE_REPO        GitHub repository (default: bornholm/conclave)
#                     CONCLAVE_BASE_URL    artifact mirror (default: the GitHub release)
set -eu

REPO="${CONCLAVE_REPO:-bornholm/conclave}"
VERSION="${CONCLAVE_VERSION:-}"
BINARY="${CONCLAVE_BINARY:-0}"
PREFIX="${CONCLAVE_PREFIX:-}"
FORCE="${CONCLAVE_FORCE:-0}"
DOWNLOAD_ONLY=0

usage() {
    cat <<'USAGE'
Installs or updates conclave from the GitHub Releases.

  curl -fsSL https://raw.githubusercontent.com/bornholm/conclave/main/install.sh | sh

Options: --version <tag>, --binary, --prefix <dir>, --force, --download-only.
Variables: CONCLAVE_VERSION, CONCLAVE_BINARY, CONCLAVE_PREFIX, CONCLAVE_FORCE,
           CONCLAVE_REPO, CONCLAVE_BASE_URL (details at the top of the script).
USAGE
}

fail() {
    echo "error: $*" >&2
    exit 1
}

while [ $# -gt 0 ]; do
    case "$1" in
    --version)
        [ $# -ge 2 ] || fail "--version needs a value"
        VERSION="$2"
        shift 2
        ;;
    --binary)
        BINARY=1
        shift
        ;;
    --prefix)
        [ $# -ge 2 ] || fail "--prefix needs a value"
        PREFIX="$2"
        BINARY=1
        shift 2
        ;;
    --force)
        FORCE=1
        shift
        ;;
    --download-only)
        DOWNLOAD_ONLY=1
        shift
        ;;
    -h | --help)
        usage
        exit 0
        ;;
    *)
        fail "unknown option: $1 (see --help)"
        ;;
    esac
done

# --- Downloader ---------------------------------------------------------------

if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL -o "$2" "$1"; }
    resolve_latest() {
        curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest"
    }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -q -O "$2" "$1"; }
    resolve_latest() {
        wget -q -O /dev/null -S "https://github.com/$REPO/releases/latest" 2>&1 |
            sed -n 's/^ *Location: \(.*\)/\1/p' | tail -1
    }
else
    fail "curl or wget is required"
fi

# --- Platform -----------------------------------------------------------------

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
linux | darwin) ;;
*) fail "unsupported OS: $OS (use the release archive by hand)" ;;
esac

case "$(uname -m)" in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
armv6l | armv7l) ARCH=armv6 ;;
i686 | i386) ARCH=386 ;;
*) fail "unsupported architecture: $(uname -m)" ;;
esac

FORMAT=binary
if [ "$BINARY" = 0 ] && [ "$OS" = linux ]; then
    if command -v dpkg >/dev/null 2>&1; then
        FORMAT=deb
        case "$(dpkg --print-architecture)" in
        amd64) ARCH=amd64 ;;
        arm64) ARCH=arm64 ;;
        armhf | armel) ARCH=armv6 ;;
        i386) ARCH=386 ;;
        *) fail "Debian architecture not covered: $(dpkg --print-architecture)" ;;
        esac
    elif command -v pacman >/dev/null 2>&1; then
        FORMAT=archlinux
    fi
fi

SUDO=""
if [ "$(id -u)" != 0 ] && [ "$DOWNLOAD_ONLY" = 0 ]; then
    if command -v sudo >/dev/null 2>&1; then
        SUDO=sudo
    elif [ "$FORMAT" != binary ]; then
        fail "run as root, install sudo, or use --binary to install without packages"
    fi
fi

if [ "$FORMAT" = binary ] && [ -z "$PREFIX" ]; then
    if [ "$(id -u)" = 0 ] || [ -n "$SUDO" ]; then
        PREFIX=/usr/local/bin
    else
        PREFIX="$HOME/.local/bin"
    fi
fi
# A user-writable prefix needs no sudo even when sudo exists.
if [ "$FORMAT" = binary ] && [ -w "$PREFIX" ] 2>/dev/null; then
    SUDO=""
fi

# --- Version ------------------------------------------------------------------

if [ -z "$VERSION" ]; then
    VERSION="$(resolve_latest || true)"
    VERSION="${VERSION##*/}"
    case "$VERSION" in
    v[0-9]*) ;;
    *) fail "cannot resolve the latest release of github.com/$REPO" ;;
    esac
fi

# Tag v0.2.0 -> 0.2.0 in file names.
BARE_VERSION="${VERSION#v}"
BASE_URL="${CONCLAVE_BASE_URL:-https://github.com/$REPO/releases/download/$VERSION}"

# --- Already installed? -------------------------------------------------------

installed_version() {
    case "$FORMAT" in
    deb) dpkg-query -W -f '${Version}' conclave 2>/dev/null || true ;;
    archlinux) pacman -Q conclave 2>/dev/null | awk '{print $2}' || true ;;
    binary)
        if [ -x "$PREFIX/conclave" ]; then
            "$PREFIX/conclave" version 2>/dev/null | awk '{print $2}' || true
        fi
        ;;
    esac
}

if [ "$FORCE" = 0 ] && [ "$DOWNLOAD_ONLY" = 0 ]; then
    # dpkg stores pre-releases with a tilde (0.2.0~next).
    INSTALLED="$(installed_version | tr '~' '-')"
    case "$INSTALLED" in
    "$BARE_VERSION" | "$BARE_VERSION"-*)
        echo "conclave $INSTALLED is already installed (--force to reinstall)"
        exit 0
        ;;
    esac
fi

# --- Download and verify ------------------------------------------------------

case "$FORMAT" in
deb) FILE="conclave_${BARE_VERSION}_linux_${ARCH}.deb" ;;
archlinux) FILE="conclave_${BARE_VERSION}_linux_${ARCH}.pkg.tar.zst" ;;
binary) FILE="conclave_${BARE_VERSION}_${OS}_${ARCH}.tar.gz" ;;
esac

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT INT TERM

echo "conclave $VERSION ($FORMAT/$OS/$ARCH)"
fetch "$BASE_URL/checksums.txt" "$WORKDIR/checksums.txt" || fail "checksums.txt not found under $BASE_URL"
echo "  downloading $FILE"
fetch "$BASE_URL/$FILE" "$WORKDIR/$FILE" || fail "$FILE not found under $BASE_URL"

grep -q "  $FILE\$" "$WORKDIR/checksums.txt" || fail "$FILE is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
    (cd "$WORKDIR" && grep "  $FILE\$" checksums.txt | sha256sum -c - >/dev/null) || fail "checksum verification failed"
elif command -v shasum >/dev/null 2>&1; then
    (cd "$WORKDIR" && grep "  $FILE\$" checksums.txt | shasum -a 256 -c - >/dev/null) || fail "checksum verification failed"
else
    fail "sha256sum or shasum is required to verify the download"
fi
echo "  checksum verified"

if [ "$DOWNLOAD_ONLY" = 1 ]; then
    cp "$WORKDIR/$FILE" "$WORKDIR/checksums.txt" "$(pwd)/"
    trap - EXIT INT TERM
    rm -rf "$WORKDIR"
    echo "verified artifact left in $(pwd) (nothing installed)"
    exit 0
fi

# --- Install ------------------------------------------------------------------

case "$FORMAT" in
deb)
    if command -v apt-get >/dev/null 2>&1; then
        $SUDO apt-get install -y --allow-downgrades "$WORKDIR/$FILE"
    else
        $SUDO dpkg -i "$WORKDIR/$FILE"
    fi
    ;;
archlinux)
    $SUDO pacman -U --noconfirm "$WORKDIR/$FILE"
    ;;
binary)
    tar -xzf "$WORKDIR/$FILE" -C "$WORKDIR" conclave
    $SUDO mkdir -p "$PREFIX"
    $SUDO install -m 0755 "$WORKDIR/conclave" "$PREFIX/conclave"
    case ":$PATH:" in
    *":$PREFIX:"*) ;;
    *) echo "  note: $PREFIX is not in your PATH" ;;
    esac
    ;;
esac

echo ""
echo "conclave $VERSION installed."
echo "  cd your-repo && conclave config example > .conclave.yaml && conclave config validate"
