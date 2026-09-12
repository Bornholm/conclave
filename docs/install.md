# Installation

Every release ships archives for Linux, macOS and Windows, Debian and Arch packages, and a `checksums.txt`.

## With the script

```bash
curl -fsSL https://raw.githubusercontent.com/bornholm/conclave/main/install.sh | sh
```

The script detects the package format of the machine, downloads one artifact, verifies its checksum and installs it. On Debian and Ubuntu that is the `.deb` through `apt-get`, on Arch and Manjaro the pacman package. Anywhere else, or with `--binary`, it extracts the binary from the archive into `/usr/local/bin`, or into `~/.local/bin` when you are not root and have no sudo.

Run it again to update. It does nothing when the installed version already matches.

| Option | Variable | Effect |
|---|---|---|
| `--version vX.Y.Z` | `CONCLAVE_VERSION` | pin a release instead of the latest |
| `--binary` | `CONCLAVE_BINARY=1` | skip the system package, install the bare binary |
| `--prefix <dir>` | `CONCLAVE_PREFIX` | where the bare binary goes, implies `--binary` |
| `--force` | `CONCLAVE_FORCE=1` | reinstall even if this version is already installed |
| `--download-only` | | fetch and verify into the current directory, install nothing |
| | `CONCLAVE_REPO` | GitHub repository, default `bornholm/conclave` |
| | `CONCLAVE_BASE_URL` | artifact mirror, default the GitHub release |

## With Go

```bash
go install github.com/bornholm/conclave/cmd/conclave@latest
```

`conclave version` prints `dev` for a `go install` build and the release tag with the commit for a packaged one.

## Requirements

`git` in the `PATH`. The agents you configure, logged in with their own credentials. For GitHub, a token in the variable named by `forge.token_env`, or the `gh` CLI logged in.
