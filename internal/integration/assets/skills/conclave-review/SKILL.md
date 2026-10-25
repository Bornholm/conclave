---
name: conclave-review
description: Run a Conclave multi-agent review on a pull request, verify its findings in the tree, and report or post them; relaunch it after fixes as an incremental review that says what was fixed, what remains and what is new. Use when asked to review a pull request with Conclave, or to relaunch a review.
---

# Conclave review loop

Conclave runs several reviewer agents plus a lead on a pull request and prints
one merged report. This skill is the loop around it: launch, wait, check the
findings before repeating them, then report or post.

## Launch detached, always

```bash
OUT=$(mktemp -d "${TMPDIR:-/tmp}/conclave-review.XXXXXX")
nohup sh -c 'conclave review "$2" > "$1/review.md" 2> "$1/review.log"; echo $? > "$1/review.done"' sh "$OUT" N >/dev/null 2>&1 &
echo "$OUT"
```

Note the directory it prints. A shell variable does not survive from one tool
call to the next in most agents, so the commands below use that literal path
wherever they say `$OUT`.

Values reach the inner shell as positional arguments, never pasted into its
script: a value holding a quote would otherwise end the string and run the rest
as a command.

A review takes 5 to 20 minutes: reviewers run in parallel, and the lead starts
once they have all ended or been stopped. A reviewer still running
`review.straggler_grace` (3 minutes) after the others have reported is stopped,
and asked for its report on `report_grace` (2 minutes more) if it had none: a
warning says so. A background-task mechanism of the agent
running this skill can kill a process that outlives the tool call; `nohup`
detaches it so it survives. Keep that mechanism for the short wait loop below,
not for Conclave itself.

Run from the repository root. Conclave reads `.conclave.yaml` there, which is
often gitignored personal tooling: never assume a collaborator has it. It also
inherits the `.conclave.yaml` of every parent directory, up to one that says
`inherits: false`, so the agents may be declared once above the project; an
agent marked `disabled: true` is dropped. The log names each inherited file
("configuration inherited"), and `conclave config validate` lists them and
says what is missing. The report goes to stdout, logs
to stderr, artifacts to `.git/conclave/runs/<run-id>/`.

## Wait on the sentinel

```bash
until [ -f "$OUT/review.done" ]; do sleep 10; done; echo "exit=$(cat "$OUT/review.done")"
```

The loop ends on its own, so it is safe to run in the background. Read the
diff or the touched files while it runs instead of idling.

A non-zero exit means the run failed: the last lines of `review.log` say why,
and the run directory they name keeps each agent's raw output.

## Check the revision before saying anything

The log line `ensuring commits ... head=<sha>` names the revision Conclave
actually reviewed. Compare it with the pull request head, fetched by its ref
rather than by branch name, which can be stale:

```bash
git fetch origin pull/N/head:conclave-pr-N -f && git log --oneline -1 conclave-pr-N
```

GitHub, Gitea and Forgejo all publish `refs/pull/N/head`. A report on a
superseded revision may describe a problem the next commit already fixed.

## Verify each finding you intend to repeat

Open the cited file and line. Conclave lists what it rejected itself under
Warnings, for instance a finding citing a path that does not exist at the head,
and the lead sometimes downgrades a severity two reviewers agreed on. A finding
you repeat is a finding you own: confirm the mechanism, not just the claim, and
read the code that consumes a value, not only the code that produces it.

A run stopped by `review.timeout` never approves, and says so under Warnings:
the verdict is then `comment` at best, whatever the reports said.

## Relaunching after fixes

Once fixes are pushed, measure the new review against the previous one rather
than starting over:

```bash
OUT=$(mktemp -d "${TMPDIR:-/tmp}/conclave-review.XXXXXX")
nohup sh -c 'conclave review "$2" --since last --responses "$1/responses.txt" > "$1/review.md" 2> "$1/review.log"; echo $? > "$1/review.done"' sh "$OUT" N >/dev/null 2>&1 &
echo "$OUT"
```

`--since last` gives the agents the previous review's findings, numbered P1
to Pn, and the diff since the head it reviewed. With that diff, the whole
pull request diff is a file in the reviewers' worktree rather than in the
prompt; without it, when the head did not move or the previous head is gone,
the whole diff is inlined as in a full review. They mark each one `fixed`,
`still-present` or `withdrawn`, and the report opens with a "Since the
previous review" section. Wait for the push to show on the forge before
launching, or the review measures the old head against itself.

Write `$OUT/responses.txt` before launching, for every previous finding you
rejected after checking it in the code: its id, then why, with the evidence.

```text
P2: deliberate. The state is per session, not per run: the continuation
starts a run of its own and must find the tools still off.
P4: wrong. ToolCallEventResult declares terminate, see the .d.ts.
```

Without it, a rejected finding comes back on every round, from scratch and
sometimes with a higher severity. With it, the agents withdraw the ones the
code bears out and keep only those your answer does not refute. Leave the
fixed ones out: the diff shows them. Drop `--responses` when there is nothing
to answer.

Check the statuses like findings: a `fixed` or `withdrawn` that is wrong takes
a real problem off the review. Open the change a `fixed` cites. Each previous
finding keeps what the reviewers said that its status overrules, under "Also
said" in the report and `claims` in the JSON: a `fixed` that a reviewer still
calls `still-present` is the first to check.

## Reporting back

Lead with the verdict and the revision, then rank by what actually matters.
On an incremental review, say first what the previous round asked for and is
now fixed or withdrawn, verified, then what remains and what is new. Do
not transcribe the report, it is already on disk: give its path and spend the
words on the two or three findings worth acting on, with the mechanism stated
concretely. Say plainly what is fine, including when nothing is wrong.

## Posting

Never post to the forge unless asked. When asked, on GitHub:

```bash
gh pr review N --request-changes --body-file comment.md
```

`--comment` for remarks without a verdict, `--approve` when clean. A structure
that reads well:

1. What the previous round asked for and is now closed, verified in the tree,
   so the author does not have to guess whether their fixes were accepted.
2. The asks, ranked, saying which ones motivate the verdict and which are cheap
   enough to fold in. One heading each, the mechanism, then the fix.
3. A short "not asking for a change" section for anything deliberate, so it is
   not read as a reproach.

`gh pr review` prints nothing on success: confirm with
`gh api repos/<owner>/<repo>/pulls/N/reviews` and check the last row.
