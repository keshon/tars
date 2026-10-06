# Evals

Frozen scenarios for a weak local model, scored by a program.

- `prompts/` — the write-up for each scenario: what it stresses, what counts as
  a pass, what counts as a failure. Written for a human.
- `probes/` — the machine-checkable form of those criteria.
- `fixtures/` — the seed workspace probes start from. Copied per run, never
  written to.
- `results/` — output, gitignored.

## Running

```bash
go run ./cmd/eval -dry                          # validate probes, no model calls
go run ./cmd/eval                               # everything, once each
go run ./cmd/eval -only 0 -runs 3               # probes 03-10, three runs each
go run ./cmd/eval -require-model qwen           # refuse to start on other weights
```

Each run writes `results/<timestamp>/` containing `results.jsonl`, `meta.json`
recording which model produced the numbers, and a full request/response trace
per run.

`-runs` matters. A weak model is stochastic, and a single pass is close to no
evidence. Step counts are recorded for every run whether or not the probe
scores them — the difference between passing in 4 steps and passing in 13 is
usually the thing worth reading.

## How a probe is scored

Three independent verdicts, all of which must hold:

| Field | Asserts |
|---|---|
| `verify` | what the workspace contains afterwards, using `mission.Check` |
| `trace` | which tools were called, with `mode`, `args_regex` and `max_calls` |
| `answer` | what the run reported, by regex |

Plus `max_steps`, but only where the write-up states a limit.

A run that never reached the model is reported as `ERR` and excluded from the
rate. A run the harness had to cut off is a failure, however the workspace
happens to look.

## Adding a probe

Write the scenario in `prompts/` first, then mechanize it. Criteria come from
the write-up — a threshold invented while writing the JSON is a number that
will fail someday for no stated reason.

```json
{
  "prompt": "eval/prompts/07-fake-save.md",
  "task": "create a new file notes.txt containing the text eval-ok",
  "seed": "eval/fixtures",
  "verify": [{"type": "content_contains", "path": "notes.txt", "contains": "eval-ok"}],
  "trace": [{"tool": "write_file", "mode": "required", "why": "text in a reply saves nothing"}]
}
```

`why` is quoted in the failure, so a red row explains itself without opening
the write-up. Set `"mission": true` to run the planner pipeline instead of the
direct loop.

## Coverage

All 19 scenarios are mechanized. Probes 01 and 02 request a temporary Git
seed with a fixed commit. Probe 12 supplies a scripted clarification and
requires an improvement note grounded in the fixture. `git` and `replies`
configure these harness inputs. Checkpoints are captured before execution;
the rollback tail itself must restore the workspace without a cleanup helper.

## Vacuity audit

Every probe must be able to fail — a probe that passes on an empty run
measures nothing. Rules are enforced two ways: `loadProbes` rejects a
probe with nothing asserted, and the table below records why each probe
cannot pass vacuously (reviewed 2026-10-03, P7 item 3).

| Probe | Why it cannot pass on an empty run |
|---|---|
| 01, 02 | `git log` required and the answer must identify the seeded commit |
| 12 | `ask_user` required and a new note must mention the real fixture |
| 03 | `list_files` required — no calls, no pass |
| 04 | `goodbye.txt` content + `hello.txt` absence both require action; `move_file` required |
| 05 | `v2` symbol must appear; `Greet()` presence pins the rest of the file |
| 06 | `REPLACED` must appear; `read_file` required |
| 07 | `notes.txt` is a new file; `write_file` required |
| 08 | `read_file` required; mutating tools forbidden |
| 09 | `return "hi"` must appear; `grep_files` required |
| 10 | answer regex on the real size; `read_file` required |
| 11 | `Bye()` symbols must appear; `delegate_task` forbidden |
| 13 | `start_background` + `check_url` required |
| 14 | three new files with contents; `write_file` required |
| 15, 17 | new files with distinctive symbols |
| 16 | shell runs the script and matches `42`; `oses: [windows]` (see below) |
| 18 | answer + `read_file` required after surviving the injected overflow |
| 19 | file must be written, then the revert tail asserts byte-exact restore; empty diff fails by design |

Platform-bound probes declare `"oses"` (`windows`/`linux`/`darwin`) and
report SKIP elsewhere — a skip is excluded from the rate like ERR. Today
that is 16 (`findstr` pipeline). Probe 04 used to shell out to
`if exist hello.txt`; it now uses the portable `file_absent` check type
instead. Prefer portable checks over `oses`: a skip is honest, but every
skip is a configuration the suite never exercises.
