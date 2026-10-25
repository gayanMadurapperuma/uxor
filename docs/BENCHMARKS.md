# Benchmarks

Measured on 2026-10-24 with `uxor eval`, on Apple Silicon. Three
public repositories, one per main language, each cloned with
`--depth 500`:

| repo | language | indexed files | commits considered → evaluated |
|---|---|---|---|
| django/django | Python | 2,982 | 157 → 100 |
| prometheus/prometheus | Go | 1,002 | 143 → 100 |
| vitejs/vite | TypeScript | 1,596 | 120 → 100 |

## Method (details in DECISIONS D25–D27)
- **Query** = a commit's subject line. **Answer** = the indexed files that
  commit changed (added files excluded).
- Skipped commits:
  - no indexable files changed (docs, CI, lockfiles): django 55,
    prometheus 43, vite 20;
  - more than 20 files: django 2.
- **Honest mode** (`--checkout-parent`): every query runs against an index
  of the commit's *parent*, built in a temporary worktree, so the answer
  can't already be in the index. Every number below is from this mode
  unless it says HEAD.
- Metrics:
  - **hook hit**: a changed file is in the prompt-hook block (500 tokens,
    at most 5 files);
  - **query hit**: the same for a `find_relevant` answer (1,000 tokens);
  - **MRR**: the mean of 1/rank of the first changed file in the full
    ranking;
  - **r@5 / r@10**: the share of all changed files in the top 5/10.

## Retrieval quality

| | django | prometheus | vite |
|---|---|---|---|
| hook hit | **71%** | **80%** | **55%** |
| query hit | 79% | 86% | 65% |
| MRR | 0.61 | 0.68 | 0.44 |
| recall@5 / @10 | 0.50 / 0.57 | 0.58 / 0.65 | 0.34 / 0.40 |
| mean tokens: hook / query block | 360 / 671 | 434 / 856 | 414 / 815 |

**Leakage check.** The same commits against an index at HEAD, which
already contains each change, score 4–8 MRR points higher:

| MRR | django | prometheus | vite |
|---|---|---|---|
| at HEAD (leaks) | 0.70 | 0.71 | 0.51 |
| honest | 0.61 | 0.68 | 0.44 |

**Tuning (D28)** raised honest MRR on both splits of every repo. The tune
split is the oldest 2/3 of the commits, the holdout the newest 1/3, never
used for choosing.

| MRR tune / holdout | before | after |
|---|---|---|
| django | 0.579 / 0.536 | 0.630 / 0.582 |
| prometheus | 0.662 / 0.657 | 0.679 / 0.676 |
| vite | 0.494 / 0.321 | 0.499 / 0.332 |
| hook hit (all) | 65% / 77% / 53% | 71% / 80% / 55% |

## Latency

| | django | prometheus | vite |
|---|---|---|---|
| retrieval in-process, hook budget, p50 / p95 | 14 / 50 ms | 15 / 21 ms | 7 / 15 ms |
| `uxor hook prompt` end to end, warm, p50 / p95 | 22 / 64 ms | 28 / 79 ms | 22 / 45 ms |
| end to end, first (cold) call | 90–145 ms | – | – |

- The deadline is 250 ms (`budgets.hook_timeout_ms`). A late hook prints
  nothing, but none was late.
- The in-process numbers come from checkout-parent runs, which re-index
  between queries. The HEAD run of django, with the same constants, measured
  p95 28 ms.

## Token savings
Tokens are chars/4 of discovery and reading only, with content as at the
parent commit. Five ways of finding and reading the code a commit touched:

- **B, perfect search**: read exactly the changed files, in full. No
  real agent does better without uxor.
- **C, uxor best case**: the answer block, plus each hunk's enclosing
  symbol ±10 lines.
- **D, uxor realistic**: the real answer.
  - If a changed file is in its top 5: the block, the C ranges of the files
    found, and full reads of those missed.
  - If not: the wasted block plus A2.
- **A2, simulated typical agent**: grep the query words (indexed files
  only), then read each match ±10 lines in up to 15 files, merged with the
  edit sites; full reads of changed files never reached.
- **A, whole files**: the same visits reading whole files. An upper bound,
  not a baseline.

| mean tokens per task | django | prometheus | vite |
|---|---|---|---|
| A2 typical agent | 169,268 | 203,089 | 90,739 |
| B perfect search | 22,851 | 54,427 | 14,079 |
| C uxor best | 6,698 | 8,519 | 3,735 |
| D uxor realistic | 53,951 | 45,485 | 47,335 |
| D median | 12,916 | 17,821 | 19,216 |
| *A whole files (bound)* | *377,208* | *590,766* | *152,105* |

**The headline, conservative comparison against perfect search.** A miss
is priced as perfect search plus the wasted block, so A2's assumptions
play no part:

| | django | prometheus | vite |
|---|---|---|---|
| tasks where uxor's top 5 has a changed file | 70% | 81% | 55% |
| on those, median tokens: uxor vs perfect search | **5.3k vs 18.5k** | **7.2k vs 32.1k** | **6.9k vs 11.6k** |
| overall saving vs perfect search | **42%** | **51%** | **21%** |
| tasks cheaper than perfect search | 64 | 81 | 40 |

**Against the simulated agent (A2):** D saves 68% (django), 78%
(prometheus) and 48% (vite), and C saves about 96%. Treat these as
indicative only:
- A2 still hits its 15-file cap in 75–87% of tasks, because short common
  words such as "ui" or "test" match thousands of lines;
- real agents choose search terms better than a word list does.

### Worked examples (django, worst → best by A2→D)
| commit | A2 | B | C | D | what happened |
|---|---|---|---|---|---|
| 82f34e2f7 *Added supports_comments skip to test_alter_…* | 20,659 | 63,187 | 63,258 | 63,258 | a hit (rank 1), but the edit's enclosing symbol is a test class spanning most of the file, so C is no cheaper than B |
| 0264b4fc3 *Made admindocs model index table full width* | 249,275 | 6,510 | 1,522 | 250,220 | a near miss: the closest changed file ranked 5th but fell outside the packed answer, so the block is wasted on top of the search |
| dffadf6df *Made distinct(\*fields) resolve aliases …* | 208,960 | 58,725 | 1,595 | 22,491 | a partial hit: 2 of 3 files found (ranks 2 and 4); `compiler.py` (rank 48) read in full |
| fb32a564c *Reverted "… system check for null kwarg in GeneratedField"* | 269,183 | 15,415 | 5,232 | 5,232 | both changed files found, at ranks 1 and 2 |
| a272e554b *Fixed GeoQuerySetTest.test_unionagg_tolerance()* | 268,271 | 9,790 | 715 | 715 | an exact identifier in the subject: rank 1, one enclosing method read |

## What this means
- When uxor finds the code, about **7 in 10 prompts** on Python and
  Go, the agent reads **3–4× fewer tokens than even a perfect search**,
  because it reads enclosing symbols instead of whole files.
- When it misses, the cost is a few hundred wasted tokens. The hook adds
  about 360–430 tokens to every prompt.
- TypeScript (vite) is the weak spot: 55% hook hits and a 21% conservative
  saving. Its commit subjects are terse ("chore: …", "fix(build): …"), and
  TS module resolution is the hardest of the three.
- Latency is far inside the deadline, and memory at query time is about
  20 MB (Phase 3).

## Caveats
- Commit subjects are not prompts. Real prompts are usually longer and
  more specific.
- Only discovery and reading tokens are counted: no reasoning, edits or
  tool-call overhead, and no saved search turns.
- Three repositories, 100 commits each. Differences of 1–2 MRR points are
  within noise.
- Shallow clones: commits at the clone boundary have no parent and are
  never evaluated.

## Phase 5.5 update (2026-10-25)

Fixes from the first real-project test (D30–D35): nested named functions,
prefix matches kept inside identifier words, synonyms, and automatic
re-parse after rule changes. The same method and 100 commits per repo:

| MRR (tune / holdout) · hook hit | before 5.5 | after 5.5 |
|---|---|---|
| django | 0.613 (0.630 / 0.582) · 71% | 0.614 (0.617 / **0.606**) · **73%** |
| prometheus | 0.678 (0.679 / 0.676) · 80% | 0.663 (0.664 / 0.660) · 79% |
| vite | 0.442 (0.499 / 0.332) · 55% | 0.448 (0.499 / **0.349**) · 55% |

On commit subjects the net effect is about neutral: holdout +0.008 and
tune −0.010 averaged over the repos. Prometheus is lower because its
`area/subarea: …` subjects leaned on the old path-token format. What
changed is what this benchmark can't see, as on the real project:
- React handlers inside components are now findable;
- "where is the login function" reaches `signIn` code;
- prompt words no longer match across identifier words
  ("handled" ≠ `handleDelete`).

The tables above this section are from before Phase 5.5.

## Answer size update (D38, 2026-10-25)

Word-aligned identifier parts plus one related line per file, averaged
over the three repos (100 commits each, honest mode):

| | hook hit | query hit | hook tokens/prompt | find_relevant tokens |
|---|---|---|---|---|
| before | 69.0% | 75.3% | 404 | 774 |
| after | 67.3% | 73.7% | **375** | **682** |

That's 7% fewer tokens on every prompt and 12% fewer per `find_relevant`
call. Hit rates are 1.6–1.7 points lower, mostly from the identifier parts
on django and prometheus. The parts are kept because they fix the real
case: "login" now reaches `AuthProvider.handleSignIn` in openverse.

## Reproduce
```bash
git clone --depth 500 https://github.com/django/django.git /tmp/django
uxor --root /tmp/django index
uxor --root /tmp/django eval --commits 100                                  # HEAD (leaks)
uxor --root /tmp/django eval --commits 100 --checkout-parent --savings     # honest + savings
make eval REPO=/tmp/django N=100                                                 # the same, via make
```
Add `--json` for per-task data and `--tuning file.json` (repeatable) to
compare constants; see TESTING §10.
