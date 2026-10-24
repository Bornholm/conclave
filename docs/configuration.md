# Configuration

[`.conclave.example.yaml`](../.conclave.example.yaml) is the full reference. `conclave config example` prints it. This page covers the settings that are easy to get wrong, then the contract an agent must honor.

## Inheritance

A `.conclave.yaml` inherits the `.conclave.yaml` files of the directories above it. Declare the agents once at the root of your projects, and in each project only what is its own:

```yaml
# ~/workspace/.conclave.yaml
version: 1
agents:
  - id: pi-reviewer
    role: reviewer
    command: [pi, -p, --mode, json]
    output: pi-json
  - id: pi-lead
    role: lead
    command: [pi, -p, --mode, json]
    output: pi-json
```

```yaml
# ~/workspace/conclave/.conclave.yaml
forge:
  provider: github
review:
  diff_exclude: ["*_templ.go"]
agents:
  - id: pi-reviewer
    timeout: 25m
```

The files are merged from the farthest to the nearest, so the nearest one wins. A mapping merges key by key: a file sets only what it names. A list replaces the inherited one, `diff_exclude` or an agent's `command` alike, with one exception: `agents` merges by `id`. An agent whose id is inherited is merged over it, field by field, and a new id is added after the inherited agents. The exception goes one way only: `agents: []` leaves the inherited agents in place. To leave out an inherited agent, give its id with `disabled: true`:

```yaml
agents:
  - id: claude
    disabled: true
```

An agent with `disabled: true` is left out whichever file declares it, so it also turns off, without deleting it, an agent a file declares itself. A `disabled` entry whose id matches no agent is an error, not a no-op: a mistyped id would leave the agent running. Two agents with the same id in one file are an error too, reported against that file. The merged result is validated as one file, so the inherited file need not be complete on its own; each file is still checked alone for unknown fields and for its `version`.

The search goes up to the filesystem root, and the home directory is not special: a `~/.conclave.yaml` applies to every project below it. `inherits: false` stops the search: the directories above that file are not read, the file itself is. The key only steers the search: it is not part of the merged configuration. Put it in a project that must not inherit anything, or in the top file to make sure nothing above it is ever read.

An inherited file can declare agents, that is commands Conclave runs. On Unix, an inherited file must belong to you or to root and be writable by its owner only, or the run stops with an error naming it: a `.conclave.yaml` another user left in `/tmp` is not run as yours. Running as root, only root's files are inherited. An inherited file you cannot read stops the run as well, rather than being skipped. Elsewhere, on Windows, an inherited file is read without that check, and `inherits: false` is the only guard. `conclave config validate` lists every file inherited, every run logs them, and the run's `manifest.json` records them in `config_sources`.

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

`review.limits` bounds what a run may cost. `max_diff_bytes` truncates the diff given to the reviewers, the agents still have the whole worktree. It cuts wherever the limit falls, so generated files and real code go alike: to keep a large diff within a model's context, exclude the generated files instead. `max_files` refuses a pull request with more changed files than that. `max_findings` caps each report. `max_issues`, `max_comments` and `max_comment_bytes` bound the discussion and the referenced issues. When there are too many comments, the most recent ones are kept.

## Diff exclusions

`review.diff_exclude` lists Git pathspec patterns left out of the diff given in the prompt: generated templates, compiled CSS, lock files. A `*` matches across directories, so `*_templ.go` covers the whole tree. The excluded files stay in the changed-files list and in the worktree, and the prompt names the patterns, so a reviewer can still read them. Generated files are worth excluding even though a large diff is no longer inlined: every reviewer still reads what is in the diff, and time spent on a generated template is time not spent on the code.

## Large diffs

A diff larger than `review.limits.max_inline_diff_bytes`, 64 KiB by default or about 1500 lines, is not inlined in the reviewer prompt. Conclave writes it to `.conclave-diff.patch` at the root of each reviewer worktree, and the prompt gives its size and an index: for each changed file, the line range of its section in that file. The reviewer is told to read it section by section, the riskiest files first, or to run `git diff` on one path, rather than loading it whole. A model handed a diff of several thousand lines in its prompt starts with its context window half full, compacts its history early and tends to lose the output contract along the way. The file lives inside the worktree because an agent sandboxed to its working directory could not read it elsewhere; it shows up as untracked in `git status`, and the prompt says it is not part of the pull request. A repository that already has a file by that name makes the reviewer fail rather than be overwritten. The index lists at most 400 files. Set `max_inline_diff_bytes` to `max_diff_bytes` to always inline the diff. `diff_exclude` still applies first: a diff made small by excluding generated files stays inline.

## Time budget

Every prompt, for reviewers, the lead, and the `triage`, `plan` and `ask` agents, states how long the agent has and the UTC time at which it is stopped: its timeout (`agent_timeout`, `lead_timeout` or its own `timeout`), or less when `review.timeout` for the whole run comes first, as it does for a reviewer that waited for a slot under `max_parallel`. The agent is asked to read the important parts first, to batch its reads, and to write its JSON as soon as it has something worth reporting rather than at the end, since a run stopped before its report produces nothing. It is not told to estimate the clock, which it cannot read: measured on real runs, a reviewer that was asked to stop near the deadline wrote 430 KB of reasoning over 18 messages and no report at all. It is not asked to check the clock: a reviewer limited to read-only tools has no command that reads it, and would spend calls looking for one.

The prompt is advice, and a model does not always take it. The same budget is therefore also exported to the agent as `CONCLAVE_DEADLINE_MS` (milliseconds, counted from the process start that Conclave's own timer uses) and `CONCLAVE_DEADLINE_AT` (RFC 3339), for an agent harness that can act on it. The Conclave extension for Pi, installed by [`conclave agents install pi`](agents.md), does: it tells the model to conclude at 50% of the budget, and blocks every tool call at 65% so that writing the answer is the only thing left to do. It leaves the stop itself to Conclave: a process that exited with status 0 at its deadline would look like a run that finished, and Conclave could not tell that it was stopped. An agent that paces itself this way has usually written its report by the time it is killed, which is what makes its output salvageable.

## Missing reports

A reviewer that exits normally but leaves no usable report, because it stopped on "now let me write the JSON", answered in prose, or produced a report that fails validation, is run once more with a short prompt: the output contract and the end of what it wrote, reasoning included (the last 64 KiB). It keeps its tools to check a line number but is told not to start over. `review.report_retries` sets how many times, 1 by default, 0 disables it. Nor is a reviewer whose model API refused every call (quota, budget, rate limit) asked again: Pi exits 0 without a word, and the provider's message becomes the reviewer's error instead. The retry prompt and output are kept as `prompts/reviewer-<id>.retry<n>.txt` and `raw/<id>.retry<n>.stdout`, and the manifest records `report_retries`.

A reviewer that wrote almost nothing before being stopped is not retried either, under 64 bytes: it ended, as "the prompt may exceed the model's context window" says when that is the likelier cause, but a stopped run is reported as such. A run that was stopped after writing its report is not a failure at all: the output of a killed process is still on disk, so Conclave parses it exactly as a completed run's and keeps the findings, with a warning that the report is the one written before the stop and the `salvaged` flag in the manifest. That is the case an agent that paces itself produces, and the difference between a usable review and none.

The same goes for the retry: a reviewer stopped with a transcript but no report is asked again, on time set aside for the purpose. `review.report_grace`, 2 minutes by default and 0 to disable, extends the deadline of that one retry past the reviewer's own timeout, which is by definition already spent: a retry inside it would be cut short the moment it starts. A retry that is stopped too goes through the same salvage.

`review.report_grace` bounds a retry and the salvage bounds the reading of a finished run. The salvage replaces a deadline that has already been spent, so that every path check does not fail and silently drop the findings; the retry gets `report_grace` past the reviewer's own timeout, but not past `review.timeout`, which stops it like any other run. Both honour a cancellation: a Ctrl-C stops them where they are. The consequence, worth stating plainly: `review.timeout` is not a hard stop. A reviewer stopped at that deadline still gets up to two minutes for its output to be read, and `max_parallel` reviewers do that at once.

When `review.timeout` expires while reviewers are still running, the reports already written or salvaged are not thrown away: there is no time left for the lead, so the review is published with the deterministic consolidation and a warning saying so. Such a review never approves: its verdict is at most `comment`. `conclave review` then exits 0, so a CI job gating on its status has to read the warnings to tell it from a complete review. A review whose reviewers left no report at all still fails. So does a review with `fail_fast` on as soon as one reviewer left nothing, even when `review.timeout` is what stopped it: `fail_fast` means that any missing report fails the review. A lead stopped after writing its consolidation is read the same way, and its verdict is likewise at most `comment`.

None of this depends on which agent runs it. The salvage reads the report where the agent left it, whether that is a bare document printed before the kill, a streamed message the run never finished, a Claude Code `result` field or an opencode text part, and closes it if the cut fell mid-write. What an agent does not get for free is a report written before Conclave stops it: `CONCLAVE_DEADLINE_MS` and `CONCLAVE_DEADLINE_AT` are exported for anything that can act on them, and the Conclave extension for Pi does. An agent that ignores them is killed on time as before, with one difference: whatever it had already printed is read instead of discarded. Configuring `claude --max-budget-usd` or a comparable limit in another harness narrows the window, but only the run itself can decide to write its report in time. An agent that exits on its own at its deadline should not: Conclave then sees a run that finished, and neither the `salvaged` flag nor `report_grace` applies.

## Running label

`review.running_label` names a label Conclave puts on the pull request once it has loaded it, and takes off when the review ends: on success, on failure, on timeout, and on interruption by Ctrl-C or SIGTERM, since the removal runs on a context of its own once the run is over. Something like `conclave/running` tells the team, or a CI job, that a review is under way. It is empty by default, which disables it. The token needs write access to the pull requests. GitHub creates the label on first use, and on Gitea Conclave creates it when the repository does not define it. Redmine has no pull requests to label, and the setting is ignored with a warning. A failure to add or remove the label is logged and never fails the review. A process killed with SIGKILL leaves the label behind, to be removed by hand.

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
