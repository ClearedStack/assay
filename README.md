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

> **Status: ready to deploy.** Everything in the specification is built and tested here. Assay speaks Muster's contract v1, replays all 52 of Muster's saved contract examples, emits evaluations that satisfy Logbook's `core.evaluation` checks and Countersign's reader, and ships hardened `cs-workers` manifests. What still depends on other components is listed under [Not yet done](#not-yet-done).

Assay is stage 03 of the ClearedStack pipeline. ClearedStack is an open pipeline that finds ideas for government-contracting tools, screens and scores them, and builds each one from the public rule. Assay is meant to be understood and used without the rest.

## What works today

Assay takes one *lease reply* (the JSON Muster hands a worker: an item, its state, and the facts Sextant observed about it) and decides, with no AI and no network:

1. **Five knockout gates**, each pass or fail with a public reason: Public source, Runs in the browser, Private by design, One job, and Arithmetic, not advice.
2. **The 100-point rubric**, for candidates that clear all five gates: seven factors rated 1 to 5 and weighted, giving a composite `score` from 20 to 100.
3. **A recommendation**: `build`, `link`, `park` or `reject`, plus a `split_flag` for split/merge review.
4. **Five tie-breaker values**, so candidates with the same score always sort the same way.

A candidate ends in one of three states: `scored`, `screened_out`, or `needs_subtool` (paused, because no rule covers it). Broken input is refused with `E_PARSE`.

As a worker, Assay asks Muster for its next item, evaluates it and reports what it decided. Muster offers the `urgent` lane first, then `normal`, then `backfill`. If Muster cannot take a finished report, Assay keeps it in a spool folder and delivers it first on the next try.

The same facts give the same outcome, byte for byte, on any machine. Assay uses only the Go standard library.

## Run it

You need Go 1.24 or later. Nothing else.

```sh
# Evaluate one lease reply and read the result
go run ./cmd/assay eval tools/rubric-profiles/pricing/fixtures/build.input.json

# The same outcome as JSON. Assay sends Muster this under "outcome", beside the evaluation Logbook records
go run ./cmd/assay eval -format json tools/rubric-profiles/pricing/fixtures/build.input.json

# Evaluate several and print the scored ones in order (or -format json)
go run ./cmd/assay rank tools/rubric-profiles/*/fixtures/build.input.json

# Verify all rules, golden fixtures, Logbook checks, row coverage and completeness
go run ./cmd/assay check

# Compare embedded rules against an edited rule folder across saved inputs
go run ./cmd/assay diff -after /path/to/edited-tools tools/rubric-profiles/pricing/fixtures/*.input.json

# Run the real worker loop over a folder of lease replies, with an in-memory stand-in for Muster
mkdir -p /tmp/assay-demo && cp tools/rubric-profiles/pricing/fixtures/*.input.json /tmp/assay-demo/
go run ./cmd/assay run -standin /tmp/assay-demo

# Run every test, including the saved examples
go test ./...
```

`eval` and `rank` also accept `-rules DIR` to try an external `tools/` tree without recompiling (`assay run` always uses the rules compiled into the binary). `eval` prints something like this:

```text
Item:     PA-4001
Category: pricing
Rules:    rubric v1, profile pricing v3
State:    scored

Knockout gates
  PASS  Public source           The rules come from officially released public material.
  PASS  Runs in the browser     It runs entirely client-side.
  PASS  Private by design       It works offline, with no account, no tracking and connect-src 'self'.
  PASS  One job                 It performs one focused job from the category's closed job vocabulary.
  PASS  Arithmetic, not advice  It computes numbers, structures data or looks up citations.

Rubric
  Need for small businesses     4 x 4 =  16
  Gap                           4 x 4 =  16
  ...
  Total                                  86 / 100

Recommendation: build
Split/merge review: no
```

`eval` exits with 0 for `scored` or `screened_out`, 3 for `E_NEEDS_SUBTOOL` and 4 for `E_PARSE`.

### As a worker

`assay run` polls a real Muster. It is configured by environment variables:

| Variable | Default | What it is |
| --- | --- | --- |
| `MUSTER_URL` | none, required | Address of Muster, for example `http://muster.clearedstack.svc.cluster.local:8080`. |
| `CONTRACT_VERSION` | `v1` | If set, must be `v1`. Assay refuses to start under any other contract. |
| `ASSAY_SPOOL_DIR` | `/var/spool/cs` | The one folder Assay writes to. |
| `ASSAY_TOKEN_FILE` | the pod's ServiceAccount token | Read afresh for every call. Set it to empty to send no token. |
| `ASSAY_POLL_INTERVAL` | `5s` | Wait after Muster had nothing to offer. |
| `ASSAY_HUB_TIMEOUT` | `10s` | Limit on each call to Muster. |

The image is built from source with [`ko`](https://ko.build); there is no Dockerfile. [`.ko.yaml`](.ko.yaml) holds the build settings and pins the static base image by digest for reproducible `linux/arm64` builds, and [`deploy/assay.yaml`](deploy/assay.yaml) the ServiceAccount, the hardened Deployment (`arm64` node affinity excluding `cm3588-plus`) and the default-deny NetworkPolicy. [`deploy/`](deploy) is a kustomize directory for Argo CD, and the top-level [`kustomization.yaml`](kustomization.yaml) points Kustomize at the same `deploy/assay.yaml`. The image is named `assay` there and nothing more: whoever deploys it gives the registry and the digest with an image override.

### What Assay says to Muster

Assay makes the five worker calls of [Muster's contract v1](https://github.com/ClearedStack/muster/blob/main/contract/v1/CONTRACT.md). Every body carries `"contract": "v1"` and only the fields that call defines.

| When | Call | Body |
| --- | --- | --- |
| Asking for work | `POST /v1/lease` | `contract` |
| The candidate cleared the gates and was scored | `POST /v1/complete` | `contract`, `lease`, `state` `scored`, and as `result` the evaluation in Logbook's form (`profile`, `gates`, `factors`, `score`, `recommendation`, `split_flag`) with the whole outcome under `outcome` |
| The candidate failed a gate | `POST /v1/complete` | the same, with `state` `screened_out` |
| No rule covers the item | `POST /v1/needs_subtool` | `contract`, `lease` |
| The lease's data was broken | `POST /v1/fail` | `contract`, `lease`, `code` `E_PARSE` |

`POST /v1/heartbeat` is implemented and tested, but the loop never needs it: scoring an item takes far less than the 60 seconds a lease lasts.

The `fail` and `needs_subtool` calls have no field for a reason, so the reason, the place and the fingerprint of an error go to Assay's own log, one JSON line per item.

A refusal from Muster is acted on by its status and its code: `E_TIMEOUT` means wait and try again; `E_PARSE` means trying again will not help, so the report is let go; a `409` means the lease is gone; `E_NEEDS_SUBTOOL` (`422`) means wait until the contract or schema matches; a refused token (`401`, `403`) is waited out. Nothing Muster writes in a refusal is repeated in Assay's logs.

## The rules are data

No threshold lives in Go code. Everything is under [`tools/rubric-profiles/`](tools/rubric-profiles), the one sub-tool listed in [`tools/registry.yaml`](tools/registry.yaml):

| Part | Where | What it holds |
| --- | --- | --- |
| Shared rubric | `rubric.yaml` | The five gates, the seven factors and their weights, the recommendation bands, the split/merge condition and the tie-breaker order. A test holds these to the numbers in `SPEC.md`. |
| Matching rule | `<category>/match.yaml` | Which items a profile takes. |
| Logic | `<category>/profile.yaml` | Closed-vocabulary fact declarations (`{value, tier}` envelopes, zero free text) and a decision table for each gate and each factor (`version: 4`). |
| Fixtures | `<category>/fixtures/` | Saved lease replies (`*.input.json`) and the exact outcome expected for each (`*.golden.json`). |

There are five profiles: `pricing`, `proposal`, `cyber`, `market` and `devsecops`.

A decision table is a list of rows, read top to bottom. The first row whose conditions all hold decides. **There is no default row.** If no row matches (including when a fact carries `tier: "Unknown"`, or when `emits_tracking` / `connect_src_self` carries `tier: "Stated"` instead of `"Seen"` or `"Designed"`), the item is paused with `E_NEEDS_SUBTOOL` and the rest of the queue carries on.

The tests enforce three things about the rules:

- Every saved input must produce its golden output exactly.
- Every row of every table in every profile must be the deciding row in at least one fixture. A rule added without a saved example fails the build.
- Every candidate that clears the gates gets a rating for every factor (checked over a large, fixed set of generated candidates).

### Adding or changing a rule

1. Edit the table in `<category>/profile.yaml`, or add a new `<category>/` folder with `match.yaml` and `profile.yaml`.
2. Add a fixture input that makes your row the deciding one.
3. Run `go run ./cmd/assay check -update` (or `go test ./tools/... -update`) to write the golden files and verify coverage and completeness, and use `go run ./cmd/assay diff -after tools ...` or `git diff` to inspect every changed verdict, score or rank before committing.
4. Run `go test ./...`.

## The three error codes

| Code | Meaning | What Assay does |
| --- | --- | --- |
| `E_NEEDS_SUBTOOL` | The data was valid and unfamiliar: an observation with no category or a category with no profile, a field or fact nobody declared, a value outside a declared set, an `Unknown` fact or unverified `Stated` privacy claim (only `Seen` or `Designed` satisfies the privacy gate), or a case no row covers. | Pauses that item with the `needs_subtool` call and carries on. Muster replays it when a new build of Assay is deployed. |
| `E_PARSE` | The data was broken: not JSON, a repeated key, a missing or mistyped field, a tier/null violation, a number out of range, a lease for something that is not an observed item. | Reports it with the `fail` call. Muster holds the item until the owner releases it. |
| `E_TIMEOUT` | Muster did not answer in time, or is not serving. | Keeps the finished report in the spool and retries, waiting longer each time (1 s doubling to 60 s). |

When data is both broken and unfamiliar, `E_PARSE` wins. An error carries a code, a fixed reason word, sometimes the name of a rule or declared fact, and a fingerprint for deduplication. It never repeats text from the input, so it cannot name a machine, an address or a credential.

## In this repository

| Path | What it is |
| --- | --- |
| [`SPEC.md`](SPEC.md) | The specification. The section "Proposed changes" at the end lists every place this build had to fill a gap, with the reason. |
| [`PROMPT.md`](PROMPT.md) | The prompt this was built from. |
| [`component.json`](component.json) | This component’s entry in the ClearedStack naming registry. |
| [`cmd/assay/`](cmd/assay) | The command: `eval`, `rank`, `check`, `diff`, `run`, `version`. |
| [`internal/contract/`](internal/contract) | The lease reply that comes in, Sextant's `{value, tier}` fact envelope, the evaluation outcome that goes out, and Logbook's `core.evaluation` checks. Strict parsing. |
| [`internal/engine/`](internal/engine) | Gates, rubric, recommendation, split/merge flag, tie-breakers, and the ordering. |
| [`internal/errs/`](internal/errs) | The three public error codes. |
| [`internal/muster/`](internal/muster) | The five calls to Muster: a small interface, the HTTP client, and an in-memory stand-in that can also be served over HTTP as a fake Muster. |
| [`internal/spool/`](internal/spool) | The on-disk buffer for reports Muster could not take. |
| [`internal/worker/`](internal/worker) | The polling loop. |
| [`internal/report/`](internal/report) | The plain-text report `eval` prints. |
| [`tools/`](tools) | The sub-tool registry, pure `io/fs` rule verification, and `rubric-profiles`: the rules and their fixtures. |
| [`deploy/`](deploy), [`kustomization.yaml`](kustomization.yaml), [`.ko.yaml`](.ko.yaml) | Kubernetes objects, Kustomize root, and `ko` build settings. |
| [`internal/yamlite/`](internal/yamlite) | A strict reader for the small part of YAML the rule files use. |
| [`testdata/contract/`](testdata/contract) | Muster's saved contract examples, copied unchanged, and replayed by `contract_test.go`. |
| [`brand/`](brand) | The Assay logo and mark, for light and dark backgrounds. |

## What has been checked

- **Saved examples:** 147 fixture pairs using clean-room `{value, tier}` envelopes, covering every row of every decision table and every planned catalog job archetype across all five `version: 4` profiles, plus text goldens for the command.
- **Muster's contract:** all 52 of Muster's example files in `contract/v1/examples/` are replayed. Where Assay can make the request, what it sends matches the example to the byte; every answer and every refusal is acted on as the contract says. The same examples are replayed against Assay's stand-in for Muster, so the thing Assay is tested against cannot drift from the contract.
- **Logbook and Countersign compatibility:** every golden `scored` and `screened_out` outcome is checked against Logbook's `core.check_evaluation` rules, Muster's `tx.Evaluation` projection, and Countersign's evaluation reader.
- **Native Go fuzzing (`testing.F`):** fuzz targets exercise lease parsing, evaluation projection, strict YAML parsing, full engine determinism, and Muster HTTP refusal handling.
- **Standard library only:** `go.mod` requires nothing, and a test fails if any Go file imports a package from outside the standard library and this module.
- **Memory:** a test builds the binary as `ko` does, pushes 6,001 leases through it over HTTP and reads peak memory from the kernel. It measured about 19 MiB against the 30 MiB budget.
- **Read-only filesystem:** the same test confirms the process writes only inside its spool folder.
- **No AI, no outside calls in the rules:** a test reads the imports of every package that decides an outcome and fails if one reaches the network, a clock, randomness or the environment.
- **Errors leak nothing:** tests plant host names, addresses and passwords in leases and in hub failures and check that none comes back in an error or a log line.
- **Pod hardening:** tests read `deploy/assay.yaml`, `kustomization.yaml` and `.ko.yaml` and check each clause of the specification's hardening rule, the bare image name, the pinned Chainguard base, the `linux/arm64` build target, node affinity and network policy.

## Not yet done

- **Sextant needs to emit `category` and `facts` on `results.sextant`.** Assay now parses Sextant's clean-room `{value, tier}` envelopes (`Seen`, `Designed`, `Stated`, `Unknown` where `value` is `null` iff `tier == "Unknown"`), optional `sub_tool` and `observed_on` provenance fields, and closed-vocabulary `core_job` slugs covering all 18 planned catalog tools plus Sextant's four base `CoreJobs` with zero free text (see "Proposed changes" in `SPEC.md`). Until Sextant's `ready-to-deploy` update emits `category` and the category's `facts` map under `results.sextant`, observations without `category` pause cleanly with `needs_subtool`.
- **`Shipkeeper` portless `kind: worker` support for `app.yaml`.** `kustomization.yaml` points directly at `deploy/assay.yaml`, while `app.yaml` remains intentionally omitted until `Shipkeeper` supports portless `kind: worker` workloads (`args: ["run"]`, `MUSTER_URL`, `/var/spool/cs` mount, and no `tcpSocket` probe).
- **One deployment value is unknown** and left out of `deploy/assay.yaml`: the name of the "Standard (Workers)" priority class.
- **Not signed or stored by the platform yet.** Signing with `cosign`, storing in Lazarette and applying with Shipkeeper are done by other components and have not been tried. The running image is built with `ko` and pinned with an image override.
- **Reporting and publishing are indirect.** Assay reports to Muster only. Muster gives Bellbuoy an incident step for each failed or paused item, and exposes results through the `pub.*` views. The reason and fingerprint of an error do not travel in Muster's calls; they are in Assay's log.
- **No liveness or readiness probe.** The specification describes none, and the network policy admits no inbound connection.

## At a glance

| | |
| --- | --- |
| **Stage** | 03 of 10 in the pipeline |
| **Formerly** | `cs-03-evaluate` |
| **Namespace** | `cs-workers` |
| **Container image** | `assay`, with the registry and digest given by a kustomize image override when deployed |
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
