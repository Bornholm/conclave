# Agents

`conclave agents install` adds to a coding agent's own configuration what makes it work with Conclave: an extension that makes a run answer before Conclave stops it, and skills that drive Conclave from an interactive session.

```bash
conclave agents install pi
conclave agents install pi --project .
conclave agents install pi --force
```

Only Pi is supported so far. `conclave agents check` is the other half: it checks that each configured agent can be launched.

## What is installed

| File | Role |
|---|---|
| `extensions/conclave.ts` | paces a run to the deadline Conclave exports |
| `skills/conclave-review/SKILL.md` | runs a review, checks the revision, verifies the findings before repeating them |
| `skills/conclave-ask/SKILL.md` | puts a question to several agents and keeps their disagreements apart |
| `skills/conclave-triage/SKILL.md` | triages issues and verifies the `fixed` and `obsolete` claims first |

The files are embedded in the binary. What is installed is always the version that matches the Conclave installing it, the names of the variables it exports or the report it expects, and an upgrade of Conclave is followed by the same command to bring them in line.

## Where

By default, in the user's configuration: `~/.pi/agent`, or the directory named by `PI_CODING_AGENT_DIR`. This is what Conclave's own runs use: Pi loads every extension of that directory, so the reviewers configured in `.conclave.yaml` need nothing on their command line.

`--project PATH` installs into `.pi/` at the root of that repository instead. Two things limit it. Pi loads a project's extensions and skills only once the project is trusted. And Conclave runs each agent in a fresh worktree at the revision under review, where only committed files exist: a project install reaches Conclave's runs only once it is committed, and is otherwise for the people working in the repository.

## Running it again

A file that already holds the bundled content is reported `unchanged`. One that differs is left alone and reported `differs`, and the command fails: it may be your own edit. `--force` replaces it.

Two warnings name files that compete with the installed ones, without touching them:

- a skill of the same name under `~/.agents/skills`, or `.agents/skills` for a project, which Pi also reads: with two skills of one name it loads only one;
- an `extensions/deadline.ts`, the standalone extension `conclave.ts` comes from, in the directory installed into or, for a project install, in the user's: with both, two extensions steer the same run.

An extension passed explicitly with `--extension` in `.conclave.yaml` is not seen by the command. Remove it once `conclave.ts` is installed.

## The Pi extension

Conclave kills an agent at its timeout, and whatever the model would have written after that is lost. Every agent run gets `CONCLAVE_DEADLINE_AT`, the instant it is stopped, and `CONCLAVE_DEADLINE_MS`, its budget (see [configuration](configuration.md)). The extension reads them and makes the run converge on an answer, at fractions of that budget counted from when Conclave launched the process, so Pi's own start-up eats into the first stage, not into the writing reserve:

| At | What happens |
|---|---|
| 50% | A steering message tells the model to stop exploring and conclude, unless it already answered. Thinking is lowered to `low`. |
| 65% | Every tool call is blocked, and thinking is lowered to `minimal`: writing the answer is the only thing left. |
| 100% | Nothing: Conclave stops the process. |

The system prompt also states the time left and when the tools go. A run that settles without having written anything after its tools are off is asked once more for its answer.

It never aborts the run and never exits by itself. Aborting mid-answer would destroy what the earlier stages worked to produce, and a process that exits with status 0 at its deadline looks like one that finished: Conclave could no longer tell it was stopped, and would neither mark the report salvaged nor give it its `report_grace`. What was written stays on standard output, where Conclave reads it.

Without either variable, outside Conclave, the extension does nothing, which is why it can be installed for every session. Its log lines go to standard error, prefixed with `[conclave]`. CI typechecks it against Pi's published types (`internal/integration/typecheck`), since nothing else in the build reads it.

A model whose provider declares it without reasoning reports its thinking as `off`, and the extension cannot lower it further, even when the model behind it, a router for instance, still reasons at length. The log says `thinking already off, left as is`: such a reviewer runs closer to its deadline, and `report_grace` is what catches it.
