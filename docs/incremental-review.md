# Incremental review

A pull request is rarely reviewed once. The author answers the first review, pushes fixes, and asks again. Reviewed from scratch, the second round says nothing about the first: whoever reads it has to work out which findings were fixed, which ones remain, and which ones come back although the author already explained why they do not hold.

`--since` measures a review against a previous one.

```bash
conclave review 42 > round1.md
# fixes pushed
conclave review 42 --since last > round2.md
conclave review 42 --since last --responses answers.txt > round3.md
```

## What `--since` takes

| Value | The previous review is |
|---|---|
| `last` | the most recent successful review of the same pull request, under `.git/conclave/runs/` |
| a run id | that run, under `.git/conclave/runs/` |
| a directory | that run directory |
| a file | that `review.json` |

The previous review must be of the same pull request, in the same repository: measured against another one's findings, every finding would come out fixed. The pull request's URL in the review and the repository in the run's manifest are checked, and `last` skips the runs of another repository. A `review.json` outside its run directory has only its URL to go by: it is refused without one, or when the forge gives the pull request none to compare, as Redmine does. Pass its run directory instead. A run that failed has no `final/review.json`, and `last` skips it.

## What the agents get

Every finding of the previous review, with an id from `P1` to `Pn`, and the diff from the previous head to the current one. The reviewers get the diff of the whole pull request as a file, `.conclave-diff.patch`, with its index, whatever its size: inlined, it would be sent again on every turn of every reviewer, while an incremental review starts from what changed since the previous one. They read it where those changes or a previous finding lead them. When there is no diff since the previous review to start from, the whole diff is given as in a full review. So it is when the repository has a file by that name, rather than failing the review. Each reviewer gives every previous finding a status:

| Status | Claims | Needs |
|---|---|---|
| `fixed` | a change since the previous review addressed it | evidence, the change |
| `still-present` | the problem is still in the code | its location at the head |
| `withdrawn` | the finding was wrong in the first place | evidence, what refutes it |

A `fixed` or `withdrawn` without evidence is ignored, the same rule `triage` applies to `fixed` and `obsolete`: either one takes a problem off the review, and a wrong one buries it. A still-present finding is reported again as a finding, at its location at the head, linked to its previous id, so the review stays complete on its own and the verdict counts it like any other.

Then the reviewers look for anything new, starting with the changes since the previous review.

When the diff is not the plain increment, the prompt says so. A head that did not move gets every previous finding checked again all the same. A branch rewritten since the previous review gives a diff that also carries changes from the base. A previous head missing from the clone is fetched, from the pull request's ref and then the remote, like the current one; one that cannot be found anywhere, after a force push for instance, gives no diff at all: the previous findings are compared with the code at the head.

With `review.include.diff: false`, the diff since the previous review stays out of the prompt as well, and the agents are given the `git diff` command instead.

## The author's responses

`--responses FILE`, or `-` for standard input, passes the author's answers to the previous review, as free text:

```text
P2: deliberate. The state is per session, not per run: the continuation
starts a run of its own and must find the tools still off.
P5: fixed in 8ff1989.
```

The agents read them like the discussion of the pull request: not as instructions, but as the record of what the author decided and why. A finding the author explains as deliberate, with a reason the code bears out, is `withdrawn`. One the response does not refute stays, however firmly the author disagrees. A problem a response settled is not raised again as new unless an agent has an argument the response does not answer, and then it says what it is.

Without them, a finding the author rejected comes back on every round, each time from scratch, sometimes with a higher severity. They are cut to `review.limits.max_comment_bytes`, like a comment of the discussion, and the review warns when they were.

## Consolidation

The lead settles each status by reading the code, not by majority, and keeps `still-present` when the code does not settle a disagreement. Without a lead, one reviewer that still sees a problem keeps it. A previous finding the lead leaves without a status is not checked, even when reviewers gave it one: a `fixed` the lead did not verify must not take a problem off the review. A warning keeps what the reviewers said. One that a finding of the lead still continues is `still-present`, with the reviewers' claims.

A status overruled is not lost. Each previous finding's `claims` lists what the reviewers said that its status and evidence do not show: another status, with its evidence and who gave it, or the same status on other evidence. Conclave fills them from the reviewers' own reports, and ignores, with a warning, any an agent writes. A status given without evidence, by the lead or by the first reviewer to give it, takes the first evidence a reviewer gave for it, and that reviewer is credited with it.

The statuses and the findings are then made to agree. A finding linked to a previous one says the problem is still there: that previous finding is `still-present`, whatever status it was given, with a warning, since a `fixed` next to a finding still in the review would tell the reader to stop looking. The status replaced goes first among its claims. A link to a previous finding with no status gives it `still-present`.

A status `still-present` that no finding carries, because the lead forgot to report it again, does not make the problem disappear: Conclave puts the previous finding back as it was written, with a warning, and a critical or high one holds the verdict at `request_changes`. When a finding of the review, in the same file and on lines that overlap, carries no link, it is the same problem reported again without one: it gets the link instead, and the problem is not shown twice.

## Output

The Markdown gets a "Since the previous review" section: how many findings were fixed, withdrawn, still present and not checked, then the fixed and withdrawn ones with their evidence, and the ones no agent gave a status. A still-present finding is marked as such where it appears. Each one shows the claims its status overruled. The JSON carries `previous_findings` with their `claims`, each finding's `previous_id`, and `meta.since` with the previous run, its head and its findings.

The run directory keeps `context/previous-review.json` and `context/since.patch`, and the manifest records `since_run_id` and `since_head_sha`.
