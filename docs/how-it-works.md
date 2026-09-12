# How a run works

`conclave review <number>` goes through these steps.

1. Load `.conclave.yaml`. An unknown field is an error, so a typo in `max_paralel` fails immediately instead of silently using the default.
2. Resolve the configured Git remote and check it belongs to the forge in the configuration.
3. Fetch the pull request, its changed files, its discussion and every issue referenced as `#N` in the description or the discussion. The discussion is comments, review summaries and inline review comments, oldest first.
4. Make the base and head commits available locally. The head comes from `refs/pull/N/head` on the configured remote, so a pull request from a fork needs no extra credentials.
5. Compute the merge base and the diff.
6. Create one detached worktree per agent at the head commit, under the system temporary directory.
7. Run the reviewers, at most `review.max_parallel` at a time. Each gets the prompt on `stdin`, or as an argument or a file, see `input` in [configuration](configuration.md).
8. Validate every report. Schema version, reviewer id, severities, paths inside the repository, line numbers and field sizes are checked. A finding in a file the pull request does not touch is kept but flagged `out_of_scope`. It gets its own section in the output and never changes the verdict.
9. Group similar findings, then run the lead with every report and the groups. The lead may only attribute a finding to a reviewer that actually succeeded. If the lead fails or prints garbage, a deterministic fallback builds the review from the groups.
10. Render Markdown or JSON, remove the worktrees, keep the artifacts.

## Artifacts

Every run lands in `.git/conclave/runs/<run-id>/`.

```
manifest.json          timings, exit codes, model per agent, warnings
context/               pull-request.json, changed-files.json, associated-issues.json, discussion.json, diff.patch
prompts/               the exact prompt each agent received
raw/                   stdout and stderr per agent, plus <id>.trace.jsonl for pi-json agents
reports/               validated reports, one per successful reviewer
final/                 groups.json, review.json, review.md
```

When a verdict looks wrong, the raw output usually shows why.

## What the prompts impose

Repository content, pull request text, issue bodies and comments go to the agents as untrusted data. The prompt says so, and says not to follow instructions found in them.

It also says the thing that pulls the other way. This untrusted material is the record of the declared scope. A problem the author has deferred to a tracked issue is reported as informational with the issue number. A problem a human reviewer already raised is reported as confirmed, not as new. A question the discussion already answers is not asked again. Before the discussion was in the prompt, the lead asked the author a question the maintainer had settled two comments earlier.

The prompt names the worktree as the only directory the agent may use. That sentence exists because OpenCode resolves the project root through the shared `.git` of a linked worktree and told its model to `cd` into the main checkout, which did not contain the pull request revision. The run ended with an empty report.

The lead's prompt is strict on purpose. It drops a finding only after verifying in the code that it is wrong. It does not lower the severity of a finding two reviewers agree on without citing the code. It judges the merged state on its own merits, so "it was already broken before" is not a reason to accept a new failure. When it hesitates between two verdicts, it takes the stricter one.

## Why several agents

On the pull request this tool was built against, no single reviewer found every real problem, and the one that found the most was not the same from one run to the next. Three reviewers with different models and tools, plus a lead that has to justify what it drops, catch more than one agent asked three times.
