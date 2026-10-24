# Agents

`conclave agents install` adds to a coding agent's own configuration what makes it work with Conclave: skills that drive Conclave from an interactive session and, for Pi, an extension that makes a run answer before Conclave stops it.

```bash
conclave agents install pi
conclave agents install claude
conclave agents install opencode
conclave agents install pi --project .
conclave agents install pi --force
```

`conclave agents check` is the other half: it checks that each configured agent can be launched.

## What is installed

| File | Role |
|---|---|
| `extensions/conclave.ts` | Pi only: paces a run to the deadline Conclave exports |
| `skills/conclave-review/SKILL.md` | runs a review, checks the revision, verifies the findings before repeating them |
| `skills/conclave-ask/SKILL.md` | puts a question to several agents and keeps their disagreements apart |
| `skills/conclave-triage/SKILL.md` | triages issues and verifies the `fixed` and `obsolete` claims first |

The files are embedded in the binary. What is installed is always the version that matches the Conclave installing it, the names of the variables it exports or the report it expects, and an upgrade of Conclave is followed by the same command to bring them in line.

Claude Code and OpenCode get the skills only. Neither has an extension yet: a reviewer run with them is stopped at its timeout without being told to conclude first, and `report_grace` is what catches its report.

## Where

| Agent | User configuration | `--project` |
|---|---|---|
| `pi` | `~/.pi/agent`, or `PI_CODING_AGENT_DIR` | `.pi/` |
| `claude` | `~/.claude`, or `CLAUDE_CONFIG_DIR` | `.claude/` |
| `opencode` | `~/.config/opencode`, or `$XDG_CONFIG_HOME/opencode` when it is absolute | `.opencode/` |

By default, the files go into the user's configuration. For Pi, this is what Conclave's own runs use: Pi loads every extension of that directory, so the reviewers configured in `.conclave.yaml` need nothing on their command line. An agent configured with `inherit_env: false` does not receive `PI_CODING_AGENT_DIR` and reads `~/.pi/agent`: pass it in the agent's `environment` in that case.

`--project PATH` installs at the root of that repository instead. Two things limit it. Pi loads a project's extensions and skills only once the project is trusted. And Conclave runs each agent in a fresh worktree at the revision under review, where only committed files exist: a project install reaches Conclave's runs only once it is committed, and is otherwise for the people working in the repository.

## Running it again

A file that already holds the bundled content is reported `unchanged`. One that differs is left alone and reported `differs`: it may be your own edit. The other files are still installed, and the command exits with status 1. `--force` replaces it. In the user's configuration, a file that is a symbolic link, into a dotfiles repository for instance, is written through and keeps its link. In a project, the link itself is replaced and never followed, and a file below a linked directory, `.pi/skills` committed as a link for instance, is reported `failed` rather than written: a repository can commit a link to any file or directory of yours.

Warnings name files that compete with the installed ones, without touching them:

- a skill of the same name in another directory the agent also reads, which loads only one of them: `~/.agents/skills` for Pi, `~/.claude/skills` and `~/.agents/skills` for OpenCode (at these paths whatever `CLAUDE_CONFIG_DIR` says, as OpenCode reads them) and the singular `skill/` below its own directory, or their `.agents/skills` and `.claude/skills` counterparts for a project, and for a project install the user's own configuration as well. A copy that holds exactly the bundled skill, no file more and none different, as an install for another agent leaves, is the same skill and is not reported;
- for Pi, an `extensions/deadline.ts`, the standalone extension `conclave.ts` comes from, in the directory installed into or, for a project install, in the user's: with both, two extensions steer the same run.

The command also reads `.conclave.yaml`, or the file named by `--config`, when it exists, and warns about an agent whose `command` still passes `deadline.ts`, with `pi --extension …/deadline.ts`, or the `--deadline-env-prefix CONCLAVE` that Conclave 0.9.0 documented for it. Remove both once `conclave.ts` is installed: the first would steer the same run twice, and Pi rejects the second once the standalone extension is gone.

A file that cannot be compared or written, a dangling symbolic link for instance, is reported `failed` with the reason. The other files are still installed and the warnings still computed, and the command fails.

## The Pi extension

Conclave kills an agent at its timeout, and whatever the model would have written after that is lost. Every agent run with a timeout gets `CONCLAVE_DEADLINE_AT`, the instant it is stopped, and `CONCLAVE_DEADLINE_MS`, its budget (see [configuration](configuration.md)). The extension reads them and makes the run converge on an answer, at fractions of that budget counted from when Conclave launched the process, so Pi's own start-up eats into the first stage, not into the writing reserve:

| At | What happens |
|---|---|
| 50% | A steering message tells the model to stop exploring and conclude, unless it already answered. Thinking is lowered to `low`. |
| 65% | Every tool call is blocked, and thinking is lowered to `minimal`: writing the answer is the only thing left. |
| 100% | Nothing: Conclave stops the process. |

The system prompt also states the time left and when the tools go. A run that settles without having written anything after its tools are off is asked once more for its answer.

It never aborts the run and never exits by itself. Aborting mid-answer would destroy what the earlier stages worked to produce, and a process that exits with status 0 at its deadline looks like one that finished: Conclave could no longer tell it was stopped, and would neither mark the report salvaged nor give it its `report_grace`. What was written stays on standard output, where Conclave reads it.

Without either variable, outside Conclave, the extension does nothing, which is why it can be installed for every session. Its log lines go to standard error, prefixed with `[conclave]`. CI typechecks it against Pi's published types (`internal/integration/typecheck`), since nothing else in the build reads it.

A model whose provider declares it without reasoning reports its thinking as `off`, and the extension cannot lower it further, even when the model behind it, a router for instance, still reasons at length. The log says `thinking already off, left as is`: such a reviewer runs closer to its deadline, and `report_grace` is what catches it.
