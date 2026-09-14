# Plan

`conclave plan <number>` turns an issue into the implementation plan a developer follows: what the ticket means in terms of this codebase, which approach to take, and the ordered steps to get there.

```bash
conclave plan 42 > plan.md
conclave plan 42 --rev origin/main --format json
```

It reuses the review machinery. Each planner gets its own worktree at `--rev`, which defaults to the current `HEAD`, reads the code, and returns one plan. The lead turns those plans into the single one that gets rendered. Nothing is ever written to the forge, and nothing is implemented: the agents are told not to modify a file.

## Several planners, one plan

Unlike `triage`, a plan run uses **every** configured reviewer by default. Deciding whether a ticket still holds rarely needs a second opinion; deciding how to build something does. Two agents that read the same code often land on different designs, and the choice between them is the interesting part.

The lead does not average them. It picks one approach, says why in the summary, and records the one it set aside under `alternatives` with the reason it lost. A plan assembled out of two half-approaches is a plan nobody wrote, and it usually does not compile as an idea.

Restrict who plans with `plan.planners`, the same way `triage.reviewers` does.

## What a step has to carry

| Field | What it holds |
|---|---|
| `title` | the change, in one line |
| `details` | what to change and why, in terms of the code that exists |
| `files` | the repository paths the step touches, created ones included |
| `validation` | how one knows the step is done: a test, a command, a behaviour |
| `depends_on` | the steps that must land first |

A step without a title or without details is dropped with a warning, and a plan left with no usable step fails, because that is not a plan. A path that escapes the repository is dropped, a `depends_on` that names something other than an earlier step is dropped, and a duplicate step id is renumbered.

Paths are checked for shape, not for existence: a step is allowed to create a file. That is also the opening for the failure this command is built around.

## The failure mode: a plan that reads well and points nowhere

A wrong triage closes a real bug. A wrong plan sends a developer to a function that does not exist, and it looks exactly like a right one — same tone, same structure, same confidence. The prompt attacks that from both ends. A planner is told to read the code first and that every name it writes must come from the worktree, except what a step creates. The lead is told to verify each file, function and package the steps name, and to turn a step that rests on something it cannot find into an open question.

The open questions are the other half. An agent that cannot tell how something works is asked to say so instead of inventing it, and a decision only a maintainer can take belongs there even when a single planner raised it. A question a maintainer already answered in the discussion does not, which is why the issue comments travel with the prompt.

`confidence` is the lead's own, not the average of the planners'. Two agents agreeing is not evidence when both guessed.

## When the lead fails

The deterministic fallback takes the most confident plan whole, keeps the other planners' approaches as alternatives and unions the open questions. It never merges steps from two approaches: that is a judgement, and without the lead there is nobody to make it. The output says so, in the summary and in `meta.lead_used`.

## Cost

One run is one agent per planner plus the lead, all over a single issue, bounded by `plan.max_parallel`. `plan.limits.max_steps` caps how long a plan may get, 30 by default, and `max_comments` and `max_references` bound the context each agent receives.
