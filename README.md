# Conclave

Conclave reviews a pull request with several local coding agents at once. Each agent gets its own disposable Git worktree at the pull request head, reads the code, and prints a JSON report. A lead agent then reads all the reports, checks them against the code, merges the duplicates and writes one Markdown review with a verdict.

It works with GitHub, github.com and Enterprise, and with Gitea or Forgejo. [Claude Code](https://docs.anthropic.com/claude-code), [OpenCode](https://opencode.ai) and [Pi](https://github.com/badlogic/pi-mono) are configured in the example file. Any command that reads a prompt and prints one JSON object can be a reviewer.

Why several agents? On the pull request this tool was built against, no single reviewer found every real problem, and the one that found the most was not the same from one run to the next. Three reviewers with different models and tools, plus a lead that has to justify what it drops, catch more than one agent asked three times.

## Installation

Every release ships archives for Linux, macOS and Windows, Debian and Arch packages, and a `checksums.txt`. The [install.sh](./install.sh) script picks the package format of the machine, verifies the checksum, then installs.

```bash
curl -fsSL https://raw.githubusercontent.com/bornholm/conclave/main/install.sh | sh
```

On Debian and Ubuntu it installs the `.deb`, on Arch and Manjaro the pacman package. Anywhere else, or with `--binary`, it puts the binary in `/usr/local/bin`, or in `~/.local/bin` when you are not root. Run it again to update. It does nothing when the version is already installed. `--version vX.Y.Z` pins a release, `--prefix <dir>` chooses where the bare binary goes, `--download-only` fetches and verifies without installing anything.

With a Go toolchain:

```bash
go install github.com/bornholm/conclave/cmd/conclave@latest
```

## Quick start

```bash
cd my-repo
conclave config example > .conclave.yaml   # then edit the agents section
export GITHUB_TOKEN=...                    # or GITEA_TOKEN
conclave config validate
conclave agents check
conclave review 123 > review.md
conclave review 123 --format json | jq .
```

Only the review goes to `stdout`. Logs go to `stderr`, so redirecting the output gives you a clean file.

## How a run works

1. Load `.conclave.yaml`. An unknown field is an error, so a typo in `max_paralel` fails immediately instead of silently using the default.
2. Resolve the configured Git remote and check it belongs to the forge in the configuration.
3. Fetch the pull request, its changed files, its discussion and every issue referenced as `#N` in the description or the discussion. The discussion is comments, review summaries and inline review comments, oldest first.
4. Make the base and head commits available locally. The head comes from `refs/pull/N/head` on the configured remote, so a pull request from a fork needs no extra credentials.
5. Compute the merge base and the diff.
6. Create one detached worktree per agent at the head commit, under the system temporary directory.
7. Run the reviewers, at most `review.max_parallel` at a time. Each gets the prompt on `stdin`, or as an argument or a file, see `input` below.
8. Validate every report. Schema version, reviewer id, severities, paths inside the repository, line numbers and field sizes are checked. A finding in a file the pull request does not touch is kept but flagged `out_of_scope`. It gets its own section in the output and never changes the verdict.
9. Group similar findings, then run the lead with every report and the groups. The lead may only attribute a finding to a reviewer that actually succeeded. If the lead fails or prints garbage, a deterministic fallback builds the review from the groups.
10. Render Markdown or JSON, remove the worktrees, keep the artifacts.

Artifacts of every run land in `.git/conclave/runs/<run-id>/`. You get the prompts, the raw agent output, the validated reports, the groups, the final review and a manifest with timings, exit codes and the model each agent ran on. When a verdict looks wrong, the raw output usually shows why.

### What the prompts say

Repository content, pull request text, issue bodies and comments go to the agents as untrusted data. The prompt says so, and says not to follow instructions found in them. It also says the thing that pulls the other way. This untrusted material is the record of the declared scope. A problem the author has deferred to a tracked issue is reported as informational with the issue number. A problem a human reviewer already raised is reported as confirmed, not as new. A question the discussion already answers is not asked again. Before the discussion was in the prompt, the lead asked the author a question the maintainer had settled two comments earlier.

The prompt names the worktree as the only directory the agent may use. That sentence exists because OpenCode resolves the project root through the shared `.git` of a linked worktree and told its model to `cd` into the main checkout, which did not contain the pull request revision. The finding count dropped to zero and the run reported an empty output.

The lead's prompt is deliberately strict. It drops a finding only after verifying in the code that it is wrong. It does not lower the severity of a finding two reviewers agree on without citing the code. It judges the merged state on its own merits, so "it was already broken before" is not a reason to accept a new failure. And when it hesitates between two verdicts, it takes the stricter one.

## Configuration

[`.conclave.example.yaml`](.conclave.example.yaml) is the full reference. The parts people get wrong:

**`forge.token_env` is the name of a variable, not the token.** For GitHub, when that variable is empty and the `gh` CLI is logged in, Conclave asks `gh auth token` for the forge host instead. `config validate` says which source it used.

**`forge.base_url` means two different things.** For GitHub it is the API root, `https://api.github.com` or `https://ghe.example.com/api/v3`. For Gitea it is the public instance URL, `https://git.example.com`, sub-path included if there is one.

**`agents[].input` decides how the prompt reaches the command.** `stdin` is the default. `argument` appends the prompt to the command line, which is what `opencode run` and `pi` expect. `file` writes the prompt to a temporary file and replaces `{prompt_file}` in the command. `{worktree}` anywhere in the command becomes the absolute worktree path, for tools with a directory flag such as `opencode run --dir {worktree}`.

**Agents inherit your environment by default.** Their credentials keep working that way. `GIT_DIR`, `GIT_WORK_TREE` and the other variables that would point Git elsewhere are always removed. `PATH`, `HOME` and `GIT_*` cannot be set in `environment` unless `allow_protected_env: true`. Set `inherit_env: false` for a minimal environment.

**`agents[].output` is `auto` or `pi-json`.** `pi-json` reads Pi's `--mode json` event stream. Conclave extracts the final answer, writes the tool calls to `raw/<id>.trace.jsonl` and records which model answered. Give Pi a larger `max_output_bytes`, the stream includes every file it reads.

**`agents[].model` is a label, not a switch.** It is written into reports and the manifest. Select the model with the tool's own flag. The model an agent reports about itself is ignored, one of them called itself `claude-sonnet-4` while running Kimi. Without a label, Conclave uses the model it can detect in the output, from the Claude Code envelope or the Pi events.

**`agents[].specialties` is optional and does not narrow the review.** Every reviewer does a complete review. Specialties add priority areas to the prompt, nothing more. A reviewer given only "performance" on a correctness fix returned zero findings, which is why the example file has none.

Exactly one agent has `role: lead`. At least one has `role: reviewer`.

## Agent contract

The agent runs with the worktree as working directory, receives the prompt, and prints one JSON object that matches the schema embedded in the prompt. The required fields are `schema_version`, `reviewer.id`, `summary`, `findings` and `verdict`. Conclave also finds the object inside the Claude Code `--output-format json` envelope, inside an NDJSON stream, and inside a ```json fenced block, so an agent that adds a sentence before its JSON still counts.

A non-zero exit code, a timeout, an empty output or an output over `max_output_bytes` marks the reviewer as failed. The run continues as long as one reviewer succeeded, and the failure is listed in the review.

## Development

```bash
go vet ./...
go test -race ./...
```

The test suite uses a fake agent in `internal/testagent` and temporary Git repositories. It never calls a real forge or a real model, so it runs in a few seconds and without credentials.

## License

MIT, see [LICENSE](./LICENSE).
