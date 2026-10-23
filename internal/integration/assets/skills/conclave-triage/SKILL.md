---
name: conclave-triage
description: Triage forge issues with Conclave, labelling each from the repository's own labels and checking against the code whether it still holds, then verify the claims before acting on them. Use when asked to triage issues, sort a backlog, or check whether issues are still valid, with conclave.
---

# Conclave triage

`conclave triage` gives each issue two answers: which of the repository's
labels it belongs to, and a status backed by evidence from the code. The agents
are the ones configured for reviews, each reads the code in its own worktree,
and a lead consolidates the batch.

## Choose the issues

```bash
conclave triage 12 13 14
conclave triage --all --state open --since 90d --limit 20
conclave triage --all --label type/bug --limit 10
conclave triage 12 --rev origin/main --format json
```

`--rev` sets the revision the code is read at, `HEAD` by default: triage
against the branch the issues are about, usually the default branch, not a
feature branch. Start with a handful of issues. A batch costs issues times
reviewers, plus one lead run.

## Run it detached

```bash
OUT=$(mktemp -d)
nohup sh -c 'out=$1; shift; conclave triage "$@" > "$out/triage.md" 2> "$out/triage.log"; echo $? > "$out/triage.done"' sh "$OUT" --all --since 90d --limit 20 >/dev/null 2>&1 &
until [ -f "$OUT/triage.done" ]; do sleep 10; done; echo "exit=$(cat "$OUT/triage.done")"
```

The arguments, a label included, reach `conclave` as they are, never pasted
into the inner script.

Run from the repository root, where `.conclave.yaml` is. The report goes to
stdout, logs to stderr.

## Check the statuses that close things

| Status | Claims | Check before repeating |
| --- | --- | --- |
| `still-present` | the behaviour is in the code | the cited file and line |
| `fixed` | a change addressed it | that the named commit or pull request does what is claimed |
| `obsolete` | the code it describes is gone | that it is gone, not moved |
| `duplicate-of` | another issue covers it | that the other issue says the same |
| `needs-info` | a human has to decide | relay the question as asked |

A wrong `fixed` buries a real problem, and nobody reopens it: these are the
claims to verify first. A `fixed` or `obsolete` the lead could not back is
downgraded to `needs-info`, with a warning.

## Applying labels

The run writes nothing on the forge by default. Only when asked:

```bash
conclave triage 12 13 --apply
```

`--apply` only adds the proposed labels, never removes one, never closes or
otherwise changes an issue, and skips issues triaged below
`triage.min_apply_confidence`. The report says, per issue, what was applied and
why the rest was not. Closing an issue stays a human decision: propose it with
the evidence, do not do it.
