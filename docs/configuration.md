# Configuration

[`.conclave.example.yaml`](../.conclave.example.yaml) is the full reference. `conclave config example` prints it. This page covers the settings that are easy to get wrong, then the contract an agent must honor.

## Forge

`forge.token_env` is the name of a variable, not the token. For GitHub, when that variable is empty and the `gh` CLI is logged in, Conclave asks `gh auth token` for the forge host instead. `config validate` says which source it used.

`forge.base_url` means two different things. For GitHub it is the API root, `https://api.github.com` or `https://ghe.example.com/api/v3`. For Gitea it is the public instance URL, `https://git.example.com`, sub-path included if there is one.

## Agents

Exactly one agent has `role: lead`. At least one has `role: reviewer`.

`input` decides how the prompt reaches the command. `stdin` is the default. `argument` appends the prompt to the command line, which is what `opencode run` and `pi` expect. `file` writes the prompt to a temporary file and replaces `{prompt_file}` in the command. `{worktree}` anywhere in the command becomes the absolute worktree path, for tools with a directory flag such as `opencode run --dir {worktree}`.

Agents inherit your environment by default, so their credentials keep working. `GIT_DIR`, `GIT_WORK_TREE` and the other variables that would point Git elsewhere are always removed. `PATH`, `HOME` and `GIT_*` cannot be set in `environment` unless `allow_protected_env: true`. `inherit_env: false` gives a minimal environment.

`output` is `auto` or `pi-json`. `pi-json` reads Pi's `--mode json` event stream. Conclave extracts the final answer, writes the tool calls to `raw/<id>.trace.jsonl` and records which model answered. Give Pi a larger `max_output_bytes`, the stream includes every file it reads. This applies to the lead as much as to the reviewers; `conclave config validate` warns about a `--mode json` agent that is missing either setting.

`model` is a label, not a switch. It is written into reports and the manifest. Select the model with the tool's own flag. The model an agent reports about itself is ignored, one of them called itself `claude-sonnet-4` while running Kimi. Without a label, Conclave uses the model it can detect in the output, from the Claude Code envelope or the Pi events.

`specialties` is optional and does not narrow the review. Every reviewer does a complete review. Specialties add priority areas to the prompt, nothing more. A reviewer given only "performance" on a correctness fix returned zero findings, which is why the example file has none.

`timeout` and `max_output_bytes` on an agent override the review-level values for that agent.

## Limits

`review.limits` bounds what a run may cost. `max_diff_bytes` truncates the diff in the prompt, the agents still have the whole worktree. `max_files` refuses a pull request with more changed files than that. `max_findings` caps each report. `max_issues`, `max_comments` and `max_comment_bytes` bound the discussion and the referenced issues. When there are too many comments, the most recent ones are kept.

## Triage

`triage.labels.source` is `forge` by default, so the taxonomy is the one the repository defines, descriptions included. `include` and `exclude` are shell globs over label names, which is how you keep `type/*` and `area/*` and drop `wontfix` or `good first issue`. `describe` fills in or overrides a description the forge left empty, and an empty description is worth fixing: the name alone tells an agent very little. `source: list` uses `labels.list` instead, for a repository with no labels yet.

`triage.reviewers` is empty by default, which means the first configured reviewer. Deciding whether a ticket still holds rarely needs three opinions the way a diff does, and a batch multiplies the cost by the number of issues.

`triage.status_labels` maps a status to a label the repository defines, and Conclave adds that label to the proposal itself rather than asking the agent to remember the rule. A mapping to a label the repository does not define is ignored with a warning. At `max_labels`, the status label replaces the last proposal, since it is the one Conclave is sure about.

`triage.limits` bounds a batch: `max_issues`, `max_comments` per issue and `max_references` per issue.

## Plan

`plan.planners` is empty by default, which means every configured reviewer. That is the opposite of `triage.reviewers`, and deliberately: two designs of the same change are worth comparing, and the lead has to choose one. Name a subset when the cost matters more than the comparison.

`plan.limits.max_steps` caps how long a plan may get, 30 by default. Steps beyond it are dropped with a warning. `max_comments` and `max_references` bound the issue context each agent receives, per issue.

`plan.use_lead: false` skips the consolidation and returns the most confident plan as it was written, with the other approaches attached as alternatives.

## Ask

`ask.respondents` is empty by default, which means every configured reviewer, for the same reason `plan.planners` is: a second opinion is what the command is for. Name a subset when the cost matters more than the comparison. Listing the same id twice is rejected, here and in `plan.planners` and `triage.reviewers`: the duplicated agent would share one working directory with itself and overwrite its own artifacts.

`ask.limits.max_question_bytes` and `max_context_bytes` bound the question and the material given with it, at 32 KiB and 512 KiB. They bound the read, so an unbounded input is cut rather than held whole in memory. `max_answer_bytes` bounds one answer, 64 KiB, since an answer is a document and not a field. `max_key_points` and `max_references` cut what an agent returns beyond them, with a warning.

`ask.use_lead: false` skips the consolidation and returns the most confident answer as it was written, with the other answers published beside it.

An ask run reads no forge, so a configuration used only for questions never needs a working token. It still needs a `forge` section to pass validation.

## Agent contract

The agent runs with the worktree as working directory, receives the prompt, and prints one JSON object that matches the schema embedded in the prompt. The required fields are `schema_version`, `reviewer.id`, `summary`, `findings` and `verdict`. Conclave also finds the object inside the Claude Code `--output-format json` envelope, inside an NDJSON stream and inside a ```json fenced block, so an agent that adds a sentence before its JSON still counts.

A non-zero exit code, a timeout, an empty output or an output over `max_output_bytes` marks the reviewer as failed. The run continues as long as one reviewer succeeded, and the failure is listed in the review.
