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

## Applying the labels

By default the run writes nothing. The report proposes labels and marks the ones that would be added, and that is all.

`--apply` adds those labels on the forge:

```bash
conclave triage --all --since 90d --apply
```

Three rules bound what it does. It only ever adds, so a label a human put there is never removed. It never closes an issue and never changes anything else about it, whatever the status says: `fixed` and `obsolete` are information for a maintainer, not an instruction. And it leaves alone any issue triaged below `triage.min_apply_confidence`, 0.6 by default, because a write to the forge is the one thing a rerun cannot undo.

The report says what happened per issue, `Applied` for the labels that landed and `Not applied` with the reason otherwise. A forge that refuses the write, a token without the right scope for instance, fails that issue and not the run.

Closing an issue stays manual. A wrong `fixed` that closes a real bug is the failure mode this whole design is built to avoid, and automating it would hand that mistake the last safeguard it has.

## Cost

A batch is issues times reviewers, plus one lead run over the whole batch. `triage.max_parallel` limits how many agent runs happen at once, and the parallelism is over the jobs, not over the agents. The default configuration uses a single reviewer for exactly this reason. Start with a handful of issues before pointing it at a hundred.
