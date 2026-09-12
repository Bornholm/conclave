# Triage

`conclave triage` answers two questions about an issue: which categories it belongs to, and whether it still describes something true about the code.

```bash
conclave triage 12 13 14 > triage.md
conclave triage --all --state open --since 90d --limit 20
conclave triage 12 --rev origin/main --format json
```

It reuses the review machinery. The agents are the ones already configured, each gets a worktree at `--rev`, which defaults to the current `HEAD`, and the lead consolidates the batch. What changes is the prompt and the output.

## Categories come from the forge

The labels are read from the repository, with their descriptions, so the taxonomy is the project's own. A repository that separates `type/bug` from `type/regression` gets that separation. One that has only `bug` and `enhancement` gets no more than that.

The description matters more than the name. `area/pipeline: nodes and pipeline execution` tells an agent what belongs there; `area/pipeline` alone leaves it guessing. Fill the empty ones in `triage.labels.describe` when you cannot fix them on the forge.

Labels that are not categories, `wontfix`, `good first issue`, `P1`, are excluded through `triage.labels.exclude`, and `include` keeps whole families with a glob. An agent that proposes a label the repository does not define has it dropped with a warning, the same way a lead cannot attribute a finding to a reviewer that did not run.

## A status needs evidence

| Status | What it claims | Evidence required |
|---|---|---|
| `still-present` | the behaviour is in the code today | a file and a line |
| `fixed` | a change addressed it | the commit or pull request, named |
| `obsolete` | the code it describes is gone | what disappeared |
| `duplicate-of` | another issue covers it | that issue number |
| `needs-info` | it cannot be decided without a human | the precise question |

A report that claims `fixed` or `obsolete` without evidence is rejected, not downgraded, and the agent counts as failed on that issue. When the lead makes the same claim, the entry is downgraded to `needs-info` and the warning says so. The asymmetry is deliberate: a wrong `fixed` buries a real problem and nobody reopens it, a wrong `needs-info` costs one question.

To make `fixed` answerable at all, the prompt carries the issue timeline, the commits and pull requests that mention the number. Without that, an agent can only guess from the current state of the code.

## What it does not do

It writes nothing to the forge. No label is applied, no issue is closed. The output is a report with proposed labels, marked with the ones that would be added, and a status per issue. Applying it is a separate decision, and for now a manual one.

## Cost

A batch is issues times reviewers, plus one lead run over the whole batch. `triage.max_parallel` limits how many agent runs happen at once, and the parallelism is over the jobs, not over the agents. The default configuration uses a single reviewer for exactly this reason. Start with a handful of issues before pointing it at a hundred.
