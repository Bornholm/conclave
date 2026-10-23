---
name: conclave-ask
description: Put one question to several agents at once with Conclave and get one answer, with the points they disagreed on kept apart. Use when asked for a second opinion, for several agents' answer to a question, or to ask a question with conclave, about a repository or not.
---

# Conclave ask

`conclave ask` gives the same question to every configured reviewer, then a
lead turns their answers into one. Where the agents disagree and the evidence
does not settle it, the split is published under `disagreements` instead of
being averaged away. It needs no forge and no ticket.

## Choose the form

| The question is about | Command |
| --- | --- |
| nothing on disk | `conclave ask "What does a Go context cancellation interrupt?"` |
| this repository | `conclave ask --project . "Where is the retry policy of the HTTP client?"` |
| another revision of it | `conclave ask --project . --rev origin/main "..."` |
| a log, a diff, a document | `conclave ask -q "What is crashing here?" --context file.log` |
| piped material | `kubectl logs deploy/api \| conclave ask -q "Why does it crash?" --context -` |

Without `--project`, the agents run in an empty directory and see no code at
all: add `--project .` whenever the answer depends on the repository. `--rev`
without `--project` is ignored, with a note.

Pass the question as an argument or with `-q`, never both. Standard input is
read for the question only when nothing else gave one, and for the context only
with `--context -`, so `--context -` needs the question on the command line.

Large material goes through `--context`, not inside the question: the question
is capped at 32 KiB and the context at 512 KiB by default, and what is over is
cut with a marker.

## Run it detached

With several agents and a lead, an answer takes minutes. Launch it so it
survives the tool call, and wait on a sentinel:

```bash
OUT=$(mktemp -d)
nohup sh -c "conclave ask --project . -q 'QUESTION' > $OUT/answer.md 2> $OUT/answer.log; echo \$? > $OUT/answer.done" >/dev/null 2>&1 &
until [ -f $OUT/answer.done ]; do sleep 10; done; echo "exit=$(cat $OUT/answer.done)"
```

`--format json` gives the structured answer instead of Markdown. The
configuration is `.conclave.yaml` in the working directory, then in
`--project`, then `~/.config/conclave/config.yaml`.

## Read the answer

- `disagreements` comes first: it is where the agents contradicted each other.
  Do not present a single answer when it is not empty; say who held which
  position and what would settle it.
- `confidence` is the lead's, not an average.
- `references` cite files and lines: open the ones the answer rests on before
  repeating it.
- `open_questions` are for the person who asked: relay them rather than guess.

When the header says the lead was unavailable, the answer is the most confident
agent's, whole, and the others are under `other_answers`, unreconciled.
