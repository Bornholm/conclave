# Conclave

Conclave reviews a pull request with several local AI coding agents running in
parallel, each inside its own disposable Git worktree, then asks a lead agent
to consolidate their JSON reports into one Markdown review.

Supported forges: GitHub (github.com and Enterprise) and Gitea / Forgejo.
Agents supported out of the box: [Claude Code](https://docs.anthropic.com/claude-code),
[OpenCode](https://opencode.ai) and [Pi](https://github.com/badlogic/pi-mono).
Any command that reads a prompt and prints one JSON object works.

## Installation

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

Only the review goes to `stdout`; logs go to `stderr`.

## How a run works

1. Load `.conclave.yaml` (unknown fields are rejected).
2. Resolve the configured Git remote and check it matches the forge.
3. Fetch the pull request, its changed files, its discussion (comments,
   reviews, inline review comments) and every issue referenced as `#N` in
   the description or the discussion.
4. Make the base and head commits available locally (`refs/pull/N/head` is
   fetched from the remote, so forks need no extra credentials).
5. Compute the merge base and the diff.
6. Create one detached worktree per agent at the head commit.
   The prompt tells agents that this material is untrusted but is the record
   of the declared scope: a problem the author deferred to a tracked issue is
   reported as informational, a problem a human already raised is marked as
   such, and questions the discussion already answers are not asked again.
7. Run the reviewers in parallel (`review.max_parallel`), each with a prompt
   on `stdin` (or as an argument / file, see `input`).
8. Validate every report: schema, reviewer id, severities, paths inside the
   repository, line numbers, sizes. Findings in files the pull request does
   not change are kept but flagged `out_of_scope`; they are rendered in their
   own section and never drive the verdict.
9. Group similar findings deterministically, then run the lead agent with all
   reports and the groups. The lead follows a conservative policy (keep
   findings unless proven wrong, judge the post-merge state on its own merits,
   explicit verdict rules) and may only attribute findings to reviewers that
   actually succeeded. If the lead fails, a deterministic fallback is used.
10. Render Markdown or JSON, remove the worktrees, keep the artifacts.

Agents get a private `TMPDIR` under the worktree scratch directory, removed with the worktrees.
Artifacts of every run are stored under `.git/conclave/runs/<run-id>/`:
prompts, raw agent output, validated reports, groups, final review, manifest.

## Configuration

See [`.conclave.example.yaml`](.conclave.example.yaml) for the full reference.
Key points:

- `forge.token_env` holds the **name** of the environment variable, never the token.
- `forge.base_url` is the API root for GitHub (`https://api.github.com`,
  `https://ghe.example.com/api/v3`) and the public instance URL for Gitea
  (`https://git.example.com`, sub-paths are supported).
- `agents[].input`: `stdin` (default), `argument` (prompt appended to the
  command, needed by `opencode run` and `pi`) or `file` (`{prompt_file}` in the
  command is replaced by a temporary file path). `{worktree}` anywhere in the
  command is replaced by the absolute worktree path, for tools with a
  directory flag (`opencode run --dir {worktree}`).
- The prompt names the worktree as the only allowed directory. Tools that
  resolve the "project root" through the shared `.git` of a linked worktree
  (OpenCode does) would otherwise point the model at the main checkout, which
  does not contain the pull request revision.
- `agents[].inherit_env` (default `true`): agents inherit the environment so
  their credentials keep working. `GIT_DIR`, `GIT_WORK_TREE` and similar are
  always stripped. Protected variables (`PATH`, `HOME`, `GIT_*`) cannot be set
  in `environment` unless `allow_protected_env: true`.
- `agents[].output`: `auto` (default) or `pi-json` for Pi's `--mode json`
  event stream. With `pi-json`, Conclave extracts the final answer, records the
  tool calls in `raw/<id>.trace.jsonl` and detects the model that answered.
- `agents[].model`: optional label recorded in reports and the manifest. The
  model an agent self-reports is never trusted; without a label Conclave uses
  the model detected in the output (Claude envelope, Pi events) when possible.
- `agents[].max_output_bytes`: per-agent override of `review.limits.max_output_bytes`.
- `agents[].specialties`: optional priority areas. Every reviewer performs a
  complete review; specialties only add emphasis and never restrict scope.
- Exactly one agent has `role: lead`; at least one has `role: reviewer`.

## Agent contract

The agent runs with the worktree as working directory, receives the prompt,
and must print one JSON object matching the schema embedded in the prompt
(`schema_version`, `reviewer.id`, `summary`, `findings[]`, `verdict`). Conclave
also extracts the object from the Claude Code `--output-format json` envelope,
from NDJSON streams and from ```json fenced blocks. A non-zero exit code, a
timeout, an empty or oversized output marks the reviewer as failed; the run
continues as long as one reviewer succeeded.

Repository content, pull request text and issue bodies are passed to agents
as untrusted data and the prompt says so explicitly.

## Development

```bash
go vet ./...
go test -race ./...
```

The test suite uses a fake agent (`internal/testagent`) and temporary Git
repositories; it never calls a real forge or model.
