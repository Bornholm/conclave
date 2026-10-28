# Loop

`conclave loop` reviews pull requests without being asked to. It watches a repository's open pull requests, reviews the ones nobody has reviewed yet, and submits each review on the forge under the token's account. It then follows those pull requests: each new head gets another round, until Conclave approves or the rounds run out.

```bash
conclave loop                      # a pass every loop.interval, until interrupted
conclave loop --once               # one pass, for a cron job or a CI schedule
conclave loop --dry-run            # one pass, the comments printed instead of submitted
conclave loop --interval 15m --max-rounds 2
conclave loop --include 172         # take up #172 although it was reviewed by hand
```

It runs from the repository's root, like `conclave review`, and reviews one pull request at a time. The token needs write access to the pull requests. GitHub and Gitea are supported. Redmine has no reviews to submit, so the command stops there.

## Which pull requests

On each pass, a pull request is taken up when:

- it is open and not a draft;
- the token's account did not open it, since a forge does not let an author approve their own work;
- nobody has reviewed it, neither someone else nor the account by hand, unless `--include` names it.

A pull request reviewed before the loop took it up is never followed, new pushes included. The loop names it in the log, `pull request skipped` with the reason `already manually reviewed`, without `--verbose`: on the first pass, then again each time its head moves. A draft is named the same way, with the reason `draft`, so that a pull request waiting to be marked ready does not look missed. The other skips are logged only with `--verbose`. What it has logged is kept in memory only, and forgotten once the pull request is no longer open, so a restarted loop, each run of `--once` and a pull request closed then reopened name it once more.

`--include N` takes up such a pull request anyway. It is repeatable and takes a list too, `--include 172,#180`; a value that is not a pull request number stops the command. The first round is a full review, and the loop follows the pull request from then on by its mark, like the ones it took up itself: the flag is spent once that round is submitted, and a mark the forge loses later does not start the rounds over. A draft or a pull request the token's account opened is still left alone. An included pull request the loop does not take up, for that reason, because it is not open, or because the loop already follows it and has nothing to do, its head unchanged since its last round for instance, is reported as a warning, `included pull request not taken up` with the reason, once per run and again each time the reason changes. `--include` only adds to the pull requests the loop takes up: the others are reviewed as without it.

Once the loop has reviewed a pull request, it follows it whoever reviews it after. It starts a new round when the head has moved since its last review. It stops when its last review approved, or when `loop.max_rounds` rounds are done, 3 by default. A pull request whose head has not moved is left alone, even if the author answered in the discussion. The answers are read at the next push.

The loop finds its own reviews again through a mark hidden in each one, an HTML comment holding the round, the head reviewed, the run and the verdict. Only the reviews of the token's account are read for marks: anyone can paste one into a review of their own. The state lives on the forge, so a loop restarted elsewhere picks up where the last one stopped.

## A round

The first round is a full review. The next ones are incremental, as with `conclave review --since`: they are measured against the previous round's run and say what was fixed, what is still there and what is new. What others wrote on the pull request since the previous round is passed as the author's responses, the newest first when they do not all fit in `review.limits.max_comment_bytes`, each cut to a quarter of it so one long answer does not crowd out the others. An incremental review needs the previous run's directory. When it is not in this clone, because the previous round ran elsewhere, the round is a full one and a warning says so.

The review's verdict becomes the forge's decision: an approval, a request for changes or a comment. The loop is stricter than the lead: an in-scope finding rated `loop.request_changes_from` or above, `low` by default, makes it request changes whatever verdict the lead gave, and a warning in the run says which finding did. A comment blocks nothing, and the loop only comes back on a new head: with the default, an in-scope finding of any severity but informational makes the loop request changes. An approval counts like any other, branch protection included. A review that was cut short by a timeout never approves, the same as with `conclave review`.

A review that fails is run at most twice on the same head, on two passes. After that the loop waits for a new push. A review the forge refuses or cannot receive is not run again: the loop keeps it and submits it again on the next pass, before deciding anything else, so a review someone submitted meanwhile does not make it lost. It is kept in memory only: a loop restarted meanwhile reviews the head again. It is given up when the head moves, when the pull request is closed or turned into a draft, or after a second refusal, after which the loop waits for a new push.

`--dry-run` writes nothing to the forge: no review, and no `review.running_label` either.

## The comment

The review is not published as `conclave review` prints it. An agent rewrites it into the comment the author reads, the lead unless `loop.writer` names another one:

- in the language of the pull request, from its description or its title, English when nothing tells;
- with the `unslop` skill applied when the agent has it, as the skills installed under `~/.agents/skills` are for Pi;
- without agent names, models or run warnings, except a warning that the review is incomplete;
- starting, in a later round, with what the author fixed since the previous one;
- saying, in the last round, that no further automatic review will follow.

The writer may not change the verdict, add a finding or drop one rated medium or above, or `loop.request_changes_from` when it is lower, since such a finding is what the request for changes stands on. The last rule is checked: a comment that does not name the file of every such finding, out-of-scope ones aside, is rejected, the one part of a finding no translation changes. The writer runs in an empty directory: the review cites the code, there is nothing to look up. The round and the last-round sentence are asked of it, not checked. `loop.max_comment_bytes` bounds the whole body the forge receives: the comment gets that much less 256 bytes kept for the mark, and is checked once cut to that size, so what is published is what was checked. The run directory keeps what the author read as `final/comment.md`, once the forge has taken it, and a comment turned down as `final/comment-rejected.md`. When the writer fails or its comment is rejected, the review is published as Conclave renders it, in English and without agent ids, models, run ids or run warnings, after one line giving the round and, on the last one, saying no further automatic review will follow, cut to `loop.max_comment_bytes`, rather than lost.

## Cost

A round costs a review plus one short agent run. A pull request costs at most `loop.max_rounds` reviews. An idle pass costs a few API calls per open pull request and no agent run.
