# Ask

`conclave ask` puts one question to several agents at once and returns one answer. Unlike `review`, `triage` and `plan`, it needs neither a forge nor a ticket, and the question does not have to be about a project.

```bash
conclave ask "What does a Go context cancellation actually interrupt?"
conclave ask --project . "Where is the retry policy of the HTTP client?"
kubectl logs deploy/api | conclave ask -q "What is crashing here, and why?" --context -
conclave ask -q "Is this migration reversible?" --context migration.sql
conclave ask --project ../other-repo --rev origin/main < question.txt
```

Only the answer goes to `stdout`. Logs go to `stderr`.

## Where the question comes from

| Form | The question is |
|---|---|
| `conclave ask "why?"` | the argument |
| `conclave ask --question "why?"` | the flag |
| `conclave ask < file` | standard input, read only because nothing else gave a question |

The flag and the argument are the same thing, and giving both is an error rather than a guess.

Standard input is never read behind your back. A question given on the command line is answered without touching it, and a process started with an inherited pipe that nobody ever closes would otherwise block in the read, before printing anything, with the question already in hand. That is not a rare shape: it is what a supervisor, a CI wrapper or another agent hands its children. When something is piped in and no `--context` asked for it, a note on `stderr` says it was ignored, so the input does not disappear silently.

## Where the context comes from

`--context FILE` reads the material the question must be answered against, and `--context -` reads it from standard input. This is what lets a log, a diff or a whole document travel with a short question instead of being pasted into a shell argument.

```bash
kubectl logs deploy/api | conclave ask -q "What is crashing here, and why?" --context -
conclave ask -q "Is this migration reversible?" --context migration.sql
```

`--context -` with no question anywhere is an error: standard input cannot be both.

`ask.limits.max_question_bytes` and `ask.limits.max_context_bytes` bound the two, at 32 KiB and 512 KiB by default. What is over the limit is cut, with a marker in the text.

An agent configured with `input: argument` passes the whole prompt on its command line, which the kernel caps at around 128 KiB on Linux. A large context needs `input: stdin` or `input: file` for that agent.

## With or without a project

Without `--project`, no repository is opened at all. Each agent runs in an empty scratch directory and the prompt says so, so nothing invites it to describe code it cannot see. The artifacts of the run go under the user cache directory, since there is no repository to sit next to.

With `--project PATH`, the question is attached to that repository. Each agent gets its own disposable Git worktree at `--rev`, which defaults to `HEAD`, exactly as a review or a plan does. The agents are told to read the code and not to modify it. Use `--project .` for the repository you are standing in.

A project does not require a forge. The remote named by `forge.remote` is resolved only to put the repository name in the header, and a repository with no remote is a perfectly valid project.

Because a question needs no repository, the configuration is looked up in three places when `--config` is not given: `.conclave.yaml` in the working directory, then in `--project`, then `conclave/config.yaml` under the user configuration directory (`~/.config/conclave/config.yaml` on Linux). A user-wide file still needs a `forge` section, which `ask` never uses.

## Several answers, one answer

Every configured reviewer answers by default, and the lead consolidates. Asking one agent is asking one agent; the point of the command is the second and third opinion. Restrict who answers with `ask.respondents`, the same way `plan.planners` does, and turn the lead off with `ask.use_lead: false`. An id listed twice is rejected at validation: the two runs would share a working directory and overwrite each other's artifacts.

The lead is told not to average the answers. Where the evidence lets it decide, it decides and says why. Where it does not, the split is published under `disagreements`, with each position and the agents that held it, and the confidence drops. An answer that quietly papers over two agents contradicting each other is the one output this command cannot afford: it costs three times a single agent and reads exactly like it.

`reported_by` and every `disagreements[].positions[].by` are filtered against the agents that actually answered, so the lead cannot attribute a position to an agent that failed or never existed.

## What an answer carries

| Field | What it holds |
|---|---|
| `answer` | the whole answer, in Markdown, self-contained |
| `key_points` | the two to five sentences a reader in a hurry keeps |
| `disagreements` | where the agents did not say the same thing, and who said what |
| `references` | what the answer is grounded in: a file with a line, a command, a document |
| `caveats` | what the answer assumes |
| `open_questions` | what only the asker can settle |
| `confidence` | how sure the lead is, not the average of the agents |

A report with no answer text fails, because everything else describes an answer that is not there. Key points and references over the limits are cut with a warning.

## When the lead fails

The deterministic fallback takes the most confident answer whole and publishes the others beside it, under `other_answers`, unreconciled. Caveats and open questions are unioned. It never merges two answers: they may contradict each other, and without the lead there is nobody to decide which one is right. The first caveat says the consolidation was deterministic, and `meta.lead_used` is false.

## Cost

One run is one agent per respondent plus the lead, bounded by `ask.max_parallel` and by the `review.*` timeouts. With `--project`, it is also one worktree per agent, removed at the end unless `--keep-worktrees` is set.
