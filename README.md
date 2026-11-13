<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/logo-dark.svg">
    <img src="brand/logo-light.svg" alt="Assay" height="56">
  </picture>
</p>

<p align="center">
  <strong>Screens each candidate against the knockout gates and scores it on the rubric.</strong><br>
  Part of <a href="https://clearedstack.com">ClearedStack</a>
</p>

---

> **Status: built, not yet deployed.** Everything in the specification is built and tested here against a stand-in for Muster. It has not yet talked to a real Muster or run in the cluster. What is still open is listed under [Not yet done](#not-yet-done).

Assay is stage 03 of the ClearedStack pipeline. ClearedStack is an open pipeline that finds ideas for government-contracting tools, screens and scores them, and builds each one from the public rule. Assay is meant to be understood and used without the rest.

## What works today

Assay takes one *observed record* (a small JSON file of facts about a candidate tool) and decides, with no AI and no network:

1. **Five knockout gates**, each pass or fail with a public reason: Public source, Runs in the browser, Private by design, One job, and Arithmetic, not advice.
2. **The 100-point rubric**, for candidates that clear all five gates: seven factors rated 1 to 5 and weighted, giving a total from 20 to 100.
3. **A recommendation**: Build, Link, Park or Reject, plus a flag for split/merge review.
4. **Five tie-breaker values**, so candidates with the same total always sort the same way.

A candidate ends in one of three states: `scored`, `screened-out`, or `needs_subtool` (paused, because no rule covers it). Broken input is refused with `E_PARSE`.

As a worker, Assay polls Muster for records lane by lane (`urgent`, then `normal`, then `backfill`), evaluates each one and hands the outcome back. If Muster cannot take a finished outcome, Assay keeps it in a spool folder and delivers it first on the next try.

The same record gives the same outcome, byte for byte, on any machine.

## Run it

You need Go 1.24 or later. Nothing else.

```sh
# Evaluate one record and read the result
go run ./cmd/assay eval tools/rubric-profiles/pricing/fixtures/build.input.json

# The same result as JSON, exactly as it would be handed to Muster
go run ./cmd/assay eval -format json tools/rubric-profiles/pricing/fixtures/build.input.json

# Evaluate several records and print the scored ones in order
go run ./cmd/assay rank tools/rubric-profiles/*/fixtures/build.input.json

# Run the real worker loop over a folder of records, with an in-memory stand-in for Muster
mkdir -p /tmp/assay-demo && cp tools/rubric-profiles/pricing/fixtures/*.input.json /tmp/assay-demo/
go run ./cmd/assay run -standin /tmp/assay-demo

# Run every test, including the saved examples
go test ./...
```

`eval` prints something like this:

```text
Item:     pricing-build
Category: pricing
Rules:    rubric v1, profile pricing v1
State:    scored

Knockout gates
  PASS  Public source           The rules come from officially released public material.
  PASS  Runs in the browser     It runs entirely client-side.
  PASS  Private by design       It works offline, with no account, no tracking and connect-src 'self'.
  PASS  One job                 It does one job, stated in one verb-first sentence.
  PASS  Arithmetic, not advice  It computes numbers, structures data or looks up citations.

Rubric
  Need for small businesses     4 x 4 =  16
  Gap                           4 x 4 =  16
  ...
  Total                                  86 / 100

Recommendation: Build
Split/merge review: no
```

`eval` exits with 0 for `scored` or `screened-out`, 3 for `E_NEEDS_SUBTOOL` and 4 for `E_PARSE`.

### As a worker

`assay run` polls a real Muster. It is configured by environment variables:

| Variable | Default | What it is |
| --- | --- | --- |
| `MUSTER_URL` | none, required | Address of Muster, for example `http://muster.clearedstack.svc.cluster.local`. |
| `CONTRACT_VERSION` | `v1` | If set, must be `v1`. Assay refuses to start under any other contract. |
| `ASSAY_SPOOL_DIR` | `/var/spool/cs` | The one folder Assay writes to. |
| `ASSAY_TOKEN_FILE` | the pod's ServiceAccount token | Read afresh for every request. Set it to empty to send no token. |
| `ASSAY_POLL_INTERVAL` | `5s` | Wait after finding every lane empty. |
| `ASSAY_HUB_TIMEOUT` | `10s` | Limit on each call to Muster. |

The image is built from source with [`ko`](https://ko.build); there is no Dockerfile. [`.ko.yaml`](.ko.yaml) holds the build settings and [`deploy/assay.yaml`](deploy/assay.yaml) the ServiceAccount, the hardened Deployment and the default-deny NetworkPolicy.

## The rules are data

No threshold lives in Go code. Everything is under [`tools/rubric-profiles/`](tools/rubric-profiles), the one sub-tool listed in [`tools/registry.yaml`](tools/registry.yaml):

| Part | Where | What it holds |
| --- | --- | --- |
| Shared rubric | `rubric.yaml` | The five gates, the seven factors and their weights, the recommendation bands, the split/merge condition and the tie-breaker order. A test holds these to the numbers in `SPEC.md`. |
| Matching rule | `<category>/match.yaml` | Which records a profile takes. |
| Logic | `<category>/profile.yaml` | The facts a record of that category must carry, and a decision table for each gate and each factor. |
| Fixtures | `<category>/fixtures/` | Saved inputs (`*.input.json`) and the exact output expected for each (`*.golden.json`). |

There are five profiles: `pricing`, `proposal`, `cyber`, `market` and `devsecops`.

A decision table is a list of rows, read top to bottom. The first row whose conditions all hold decides. **There is no default row.** If no row matches, the item is paused with `E_NEEDS_SUBTOOL` and the rest of the queue carries on.

The tests enforce three things about the rules:

- Every saved input must produce its golden output exactly.
- Every row of every table in every profile must be the deciding row in at least one fixture. A rule added without a saved example fails the build.
- Every candidate that clears the gates gets a rating for every factor (checked over a large, fixed set of generated candidates).

### Adding or changing a rule

1. Edit the table in `<category>/profile.yaml`, or add a new `<category>/` folder with `match.yaml` and `profile.yaml`.
2. Add a fixture input that makes your row the deciding one.
3. Run `go test ./tools/... -update` to write the golden files, and **read the diff** before committing. The diff is the change in behaviour.
4. Run `go test ./...`.

## The three error codes

| Code | Meaning | What Assay does |
| --- | --- | --- |
| `E_NEEDS_SUBTOOL` | The data was valid and unfamiliar: a category with no profile, a field or fact nobody declared, a value outside a declared set, or a case no row covers. | Pauses that item in `needs_subtool` and carries on. |
| `E_PARSE` | The data was broken: not JSON, a repeated key, a missing or mistyped field, a number out of range. | Refuses that item and reports the error to Muster. |
| `E_TIMEOUT` | Muster did not answer in time, or is not serving. | Keeps the finished outcome in the spool and retries, waiting longer each time (1 s doubling to 60 s). |

When data is both broken and unfamiliar, `E_PARSE` wins. An error carries a code, a fixed reason word, sometimes the name of a rule or declared fact, and a fingerprint for deduplication. It never repeats text from the input, so it cannot name a machine, an address or a credential.

## In this repository

| Path | What it is |
| --- | --- |
| [`SPEC.md`](SPEC.md) | The specification. The section "Proposed changes" at the end lists every place this build had to fill a gap, with the reason. |
| [`PROMPT.md`](PROMPT.md) | The prompt this was built from. |
| [`component.json`](component.json) | This component’s entry in the ClearedStack naming registry. |
| [`cmd/assay/`](cmd/assay) | The command: `eval`, `rank`, `run`, `version`. |
| [`internal/contract/`](internal/contract) | The v1 record that comes in and the outcome that goes out. Strict parsing. |
| [`internal/engine/`](internal/engine) | Gates, rubric, recommendation, split/merge flag, tie-breakers, and the ordering. |
| [`internal/errs/`](internal/errs) | The three public error codes. |
| [`internal/muster/`](internal/muster) | The small interface to Muster, an in-memory stand-in, and the HTTP client. |
| [`internal/spool/`](internal/spool) | The on-disk buffer for outcomes Muster could not take. |
| [`internal/worker/`](internal/worker) | The polling loop. |
| [`internal/report/`](internal/report) | The plain-text report `eval` prints. |
| [`tools/`](tools) | The sub-tool registry and `rubric-profiles`: the rules and their fixtures. |
| [`deploy/`](deploy), [`.ko.yaml`](.ko.yaml) | Kubernetes objects and the `ko` build settings. |
| [`third_party/`](third_party) | The licence and notice of the one dependency, a YAML parser. |
| [`brand/`](brand) | The Assay logo and mark, for light and dark backgrounds. |

## What has been checked

- **Saved examples:** 111 fixture pairs, covering every row of every decision table, plus text goldens for the command.
- **Memory:** a test builds the binary as `ko` does, pushes 6,001 records through it over HTTP and reads peak memory from the kernel. It measured about 17 MiB against the 30 MiB budget.
- **Read-only filesystem:** the same test confirms the process writes only inside its spool folder.
- **No AI, no outside calls in the rules:** a test reads the imports of every package that decides an outcome and fails if one reaches the network, a clock, randomness or the environment.
- **Errors leak nothing:** tests plant host names, addresses and passwords in records and in hub failures and check that none comes back in an error or a log line.
- **Pod hardening:** tests read `deploy/assay.yaml` and check each clause of the specification's hardening rule.
- **Image build:** `ko build` produced an image from source (about 3.7 MB, running as user 65532) with the same digest on repeated builds. It was not pushed anywhere.

## Not yet done

- **No real Muster.** Muster does not exist yet. Assay has only run against the stand-in in `internal/muster`. The record shape, the outcome shape and the HTTP calls are this repository's proposal (see "Proposed changes" in `SPEC.md`) and must be agreed with Muster and Sextant.
- **The thresholds are placeholders.** `SPEC.md` fixes the gates, the weights and the recommendation bands, and those are implemented exactly. It does not say what earns a 3 rather than a 4 on any factor. The thresholds and verb lists in the five profiles are made up so the engine has something to run. They need a decision by the project owner before any score is published.
- **Three deployment values are unknown** and marked provisional in `deploy/assay.yaml`: Muster's address and port, the labels on Muster's pods, and the name of the "Standard (Workers)" priority class (left out).
- **Never deployed.** Signing with `cosign`, storing in Lazarette and applying with Shipkeeper are done by other components and have not been tried.
- **Reporting and publishing are indirect.** Assay hands every outcome and error to Muster. Opening a Bellbuoy task and exposing results through the `pub.*` views are left to those components, because the network policy lets Assay reach only Muster.
- **No liveness or readiness probe.** The specification describes none, and the network policy admits no inbound connection.

## At a glance

| | |
| --- | --- |
| **Stage** | 03 of 10 in the pipeline |
| **Formerly** | `cs-03-evaluate` |
| **Namespace** | `cs-workers` |
| **Container image** | `lazarette.clearedstack.svc.cluster.local:5000/assay` |
| **Memory budget** | 30 MiB |
| **Depends on** | [Muster](https://github.com/ClearedStack/muster) |
| **Depended on by** | Nothing else in the platform |
| **Rules for particular cases** | `rubric-profiles` |

### Boundaries

- Leases observed records from Muster
- Returns a pass or fail on each gate, a score and a recommendation

## Names

`assay` is this component’s identifier everywhere: the repository, the workload, the container image. The one place names are written down is the [naming registry](https://clearedstack.com/platform/registry.json), kept in [`clearedstack.com`](https://github.com/ClearedStack/clearedstack.com/blob/main/src/data/registry.json). `component.json` here is a copy of this component’s entry. If the two disagree, the registry is right.

## Ground rules

- **No AI when it runs.** The same input gives the same output on any machine.
- **Stop instead of guessing.** A case with no rule is paused and reported, never improvised.
- **Clean room.** Written from scratch. No code, text or design is copied from another project.
- **Public sources and made-up data only.** Nothing classified, controlled or taken from a real contract, in code, tests, issues or commit messages.

## License

[MIT](LICENSE). ClearedStack is an independent personal project. It is not affiliated with, endorsed by, or sponsored by the U.S. Department of Defense or any other government agency.
