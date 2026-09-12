# Conclave

Conclave reviews a pull request with several local coding agents at once. Each agent gets its own disposable Git worktree at the pull request head, reads the code and prints a JSON report. A lead agent checks the reports against the code, merges the duplicates and writes one Markdown review with a verdict.

It works with GitHub and with Gitea or Forgejo. [Claude Code](https://docs.anthropic.com/claude-code), [OpenCode](https://opencode.ai) and [Pi](https://github.com/badlogic/pi-mono) are configured in the example file. Any command that reads a prompt and prints one JSON object can be a reviewer.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/bornholm/conclave/main/install.sh | sh
```

The script installs the Debian or Arch package when it can, the bare binary otherwise, and verifies the checksum first. Options and the `go install` alternative are in [docs/install.md](docs/install.md).

## Use

```bash
cd my-repo
conclave config example > .conclave.yaml   # then edit the agents section
conclave config validate
conclave review 123 > review.md
```

Only the review goes to `stdout`. Logs go to `stderr`.

The same agents also triage issues. Each one is labelled from the taxonomy the forge defines, and gets a status that has to be backed by evidence: still present, fixed, obsolete, duplicate or needs information.

```bash
conclave triage 12 13 14 > triage.md
conclave triage --all --since 90d --limit 20
conclave triage --all --apply          # adds the proposed labels, never closes
```

## Read more

- [How a run works](docs/how-it-works.md), from the fetch to the artifacts, and what the prompts impose on the agents.
- [Triage](docs/triage.md), what a status means and what it takes to earn it.
- [Configuration](docs/configuration.md), the settings people get wrong and the contract an agent must honor.
- [`.conclave.example.yaml`](.conclave.example.yaml), the full reference with the three agents.

## Development

```bash
go vet ./...
go test -race ./...
```

The test suite uses a fake agent and temporary Git repositories. It never calls a real forge or a real model.

## License

MIT, see [LICENSE](./LICENSE).
