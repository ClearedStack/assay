# Assay specification

> Technical specification for the deterministic Assay worker that executes five binary knockout gates, the 100-point rubric, and five tie-breakers using YAML/Go rules.

**Status: design.** This is a specification. It describes how Assay is meant to work, not something that exists yet.

The same text is published at [clearedstack.com/platform/assay/](https://clearedstack.com/platform/assay/). To change the specification, change it here and say why.

## Overview & Role

Assay (`v0.1.0-draft`) is the deterministic screening and scoring worker in ClearedStack v3.1, replacing the original Stages 4 (Screen) and 5 (Score). Implemented as an MIT-licensed Go service in the Assay repository, it evaluates `observed` candidates using a pure YAML/Go rule engine with zero AI models and zero token costs.

Given identical observed facts, Assay produces the exact same gate outcome, 100-point rubric score, preliminary recommendation, and tie-breaker ordering on any cluster.

## Runtime & Resource Budget

Assay runs in the `cs-workers` Kubernetes namespace under the **Standard (Workers)** priority class.

| Attribute | Value |
| --- | --- |
| **Worker ID / Repo** | Assay (`assay`) |
| **Namespace** | `cs-workers` |
| **Kind / Version** | `worker` · `v0.1.0-draft` |
| **RAM Budget** | `30 MiB` (always-on Go pod) |
| **Runtime AI Calls** | `0` (100% deterministic YAML/Go rule engine) |
| **Local Spool** | `/var/spool/cs` (buffers completed scoring payloads if Muster restarts) |
| **Build & Deploy** | Built with `ko` (no Docker) by Keelworks, signed with `cosign` in Lazarette, deployed by Shipkeeper via Server-Side Apply |

## Interfaces & State Transitions

Assay polls Muster in the `clearedstack` namespace over ServiceAccount-authenticated cluster-private HTTP (`CONTRACT_VERSION=v1`), draining `urgent` → `normal` → `backfill` lanes.

- **Inbound state:** `observed` (structured JSON facts produced by Sextant).
- **Evaluation stages:**
  1. **Five binary knockout gates:**
     - *Public source* — Rules, formulas, and thresholds must come from officially released public materials (statutes, FAR, DFARS, NIST SPs).
     - *Runs in the browser* — Must execute entirely client-side (HTML/CSS/JS/WASM) with no backend server or remote API.
     - *Private by design* — Must work offline, require no accounts, emit zero tracking, and enforce `connect-src 'self'`.
     - *One job* — Must perform a single focused task describable in one active verb-first sentence.
     - *Arithmetic, not advice* — Must compute numbers, structure data, or look up citations—never render legal advice.
  2. **100-point rubric:** Candidates clearing all five gates are rated `1`–`5` across seven weighted factors (composite `20`–`100`): Need for small businesses (`×4`, 20 pts), Gap (`×4`, 20 pts), Strength of public source (`×3`, 15 pts), Fits in a browser (`×3`, 15 pts), Small enough to read (`×2`, 10 pts), Can be checked (`×2`, 10 pts), and Low upkeep of reference data (`×2`, 10 pts).
  3. **Preliminary recommendation & split/merge flag:** Assigns `Build` (Score ≥ 75 and Gap > 1), `Link` (Score ≥ 75 and Gap = 1), `Park` (Score 55–74), or `Reject` (Score < 55). Flags **split/merge review** when Need is `4`–`5` and Small enough to read is `1`–`2`.
  4. **Five deterministic tie-breakers:** Roadmap phase fit → Gap severity → Low upkeep → Small code footprint → Community demand.
- **Outbound transitions:**
  - `screened_out` — Candidate failed one or more knockout gates (failure reason logged publicly).
  - `scored` — Candidate cleared all five gates and received its rubric breakdown and recommendation.
  - `needs_subtool` — Fail-closed pause (`E_NEEDS_SUBTOOL`) when an item's category lacks a matching rubric profile.

## Sub-Tools & Failure Modes

Domain-specific scoring thresholds and fixtures live in Assay under `tools/rubric-profiles/<category>/` (such as `pricing`, `proposal`, `cyber`, `market`, and `devsecops`).

| Sub-Tool | Responsibility |
| --- | --- |
| `rubric-profiles` | Category-specific YAML/Go rule sets and golden test fixtures defining deterministic gate checks and rubric factor mappings. |

### Sanitized Failure Modes

Assay fails closed on unknown fields or unsupported categories rather than falling back to defaults or heuristics:

| Error Code | Trigger | Platform Behavior |
| --- | --- | --- |
| `E_NEEDS_SUBTOOL` | Observed item carries a contracting category or unknown field not covered by `tools/rubric-profiles/<category>/`. | Pauses item in `needs_subtool`. Bellbuoy opens a deduplicated task in Assay on Plainforge; once a new profile and fixture deploy, Muster replays paused items automatically. |
| `E_PARSE` | Observed JSON payload fails contract schema validation or contains invalid types. | Halts item cleanly and reports a fingerprinted error via Bellbuoy. |
| `E_TIMEOUT` | Hub lease or completion call times out. | Completed score is retained in `/var/spool/cs` and retried on backoff. |

## Security & Clean-Room Boundaries

- **Zero raw observation access:** Assay operates strictly on sanitized structured JSON facts from `observed` records; it never reads raw vendor HTML, screenshots, or binary PDFs.
- **Public transparency:** All knockout gate outcomes, rubric weights, factor scores, and tie-breaker results are written through Muster to `core.*` and exposed read-only via `pub.*` views on `clearedstack.com` with no login wall.
- **Pod hardening & network policy:** Runs as an unprivileged non-root container with dropped capabilities, read-only root filesystem (writable only at `/var/spool/cs`), zero database credentials, and default-deny `NetworkPolicy` permitting only ServiceAccount HTTP to Muster.

## Related pages

- [How we decide what to build](https://clearedstack.com/platform/how-we-decide/)
- [An item’s life: fourteen states and three lanes](https://clearedstack.com/platform/item-lifecycle/)
- [The pipeline and its ten workers](https://clearedstack.com/platform/pipeline/)
- [Operating it: status views, failures and backups](https://clearedstack.com/platform/operations/)

## Proposed changes

> **These are proposals, not yet agreed.** Assay was built from this specification in this repository. Where the text above was silent, contradicted itself or could not be built as written, the build had to choose. Each choice is written down here with its reason, so it can be accepted into the text above, changed or rejected. The code follows these proposals today.
>
> One line above this heading has been edited: the state `screened-out` is now spelled `screened_out`, to match Muster's contract v1, which is the one place state names are defined.

### 1. Follow Muster's contract v1 for every call

**Gap:** the specification says Assay polls Muster over HTTP but gives no paths, bodies or status codes.

**Resolution:** these are now defined by Muster, in `contract/v1/CONTRACT.md` in the Muster repository, and Assay follows that file. Nothing about the calls is defined here any more. In short:

- Every request body carries `"contract": "v1"` and only the fields the call defines. The ServiceAccount token is read afresh for every call.
- `POST /v1/lease` with `contract` alone. Muster answers `204`, or `200` with `lease`, `kind`, `id`, `state`, `lane`, `attempt`, `expires`, `results` and sometimes `found`. Muster chooses the lane: `urgent`, then `normal`, then `backfill`.
- `POST /v1/complete` with `lease`, `state` (`scored` or `screened_out`) and the outcome as `result`.
- `POST /v1/needs_subtool` with `lease`, when no rule covers the item.
- `POST /v1/fail` with `lease` and `code` `E_PARSE`, when the lease's data is broken.
- `POST /v1/heartbeat` is implemented, but the loop never needs it, because scoring an item takes far less than a lease lasts.

Muster's saved examples are copied into `testdata/contract/` and replayed by a test.

### 2. Define what Sextant leaves for Assay to score

**Gap:** the specification says Assay scores "structured JSON facts produced by Sextant" but never says what they look like. Muster's contract carries each worker's result as an opaque JSON object, so this is between Sextant and Assay. Without it no gate or factor has anything to read.

**Proposal:** Assay scores `results.sextant` in the lease, which holds exactly two fields:

| Field | Type | Rule |
| --- | --- | --- |
| `category` | string | Lower-case token, for example `pricing`. Selects the rubric profile. |
| `facts` | object | Flat map of fact name to value. A value is a boolean, a whole number, a string, or a list of strings. Nothing else. |

Which facts an observation must carry is declared by the category's profile (`tools/rubric-profiles/<category>/profile.yaml`, under `facts`), with a type and limits for each. An observation must carry every declared fact and no other. Fractions, nulls, nested objects and repeated keys are refused. Results left by other workers, and `found`, are ignored.

**Reason:** whole numbers and closed sets keep the outcome identical on every machine. Letting the profile declare the facts is what makes "unknown field not covered by `tools/rubric-profiles/<category>/`" checkable.

**Not yet agreed with Sextant.** Sextant's first build writes a different shape: nine tiered facts about a page, and no category. Assay treats such an observation as valid and unfamiliar and pauses the item (see 4). Until the two components agree, no real item will be scored.

### 3. Define the outcome Assay hands back

**Gap:** the specification lists what is returned (gate results, a score, a recommendation) but not its shape.

**Proposal:** the `result` of a `complete` call is one JSON object: `contract`, `worker` (`assay`), `id`, `state`, `category`, `rules` (the rubric version and the profile name and version that decided), `gates` (five entries: `id`, `name`, `pass`, `rule`, `reason`), `factors` (seven entries: `id`, `name`, `rating`, `weight`, `points`, `rule`), `total`, `recommendation`, `split_merge_review` and `tie_breakers` (five entries: `id`, `name`, `value`). `rule` is the row of the decision table that decided, so any published verdict can be traced to one line of rule data. The golden files under `tools/rubric-profiles/` are the full examples.

A candidate that is screened out carries all five gate results and no score.

### 4. Say which error an unknown field is

**Contradiction:** the failure table gives an "unknown field" `E_NEEDS_SUBTOOL`, but an unknown field also "fails contract schema validation", which the next row gives `E_PARSE`.

**Proposal:** broken data is `E_PARSE`: not JSON, a repeated key, a missing or mistyped field, a number out of range, a lease for anything but an observed item. Well-formed data that is merely unfamiliar is `E_NEEDS_SUBTOOL`: an observation that names no category or a category with no profile, a field or fact nobody declared, a value outside a declared set, or a case no rule row covers. When a lease is both, `E_PARSE` is reported.

**Reason:** this matches the one-line meanings of the codes ("the data was broken" against "the data was valid and unfamiliar"), and an item can only be called unfamiliar once it is known to be valid. It also matters in practice: Muster holds an `E_PARSE` item until the owner releases it, but replays a paused item by itself when a new build of Assay is deployed.

### 5. Accept that an error's detail does not reach Bellbuoy

**Gap:** the failure table says an `E_PARSE` item is reported "via Bellbuoy" with a fingerprint. Muster's `fail` and `needs_subtool` calls carry no field for one, and Muster gives Bellbuoy an incident step that names only the item, the worker, the code and the state.

**Proposal:** Assay writes the reason, the place and the fingerprint of every error to its own log, as one JSON line per item, and sends Muster only what the contract defines. If Bellbuoy needs the fingerprint to deduplicate tasks, that is a change to Muster's contract, not something Assay can add.

### 6. Say what the tie-breakers read and how they sort

**Gap:** the five tie-breakers are named but not defined. Two of them (roadmap phase fit, community demand) are not rubric factors, and Assay sees one item at a time, so it cannot itself put candidates in order.

**Proposal:**

- Each scored outcome carries the five values, in order. Anything that ranks candidates sorts by total, then by each value in turn, higher first.
- Gap severity is the Gap rating, Low upkeep is the Low-upkeep rating, and Small code footprint is the Small-enough-to-read rating.
- Roadmap phase fit and Community demand are whole-number facts in the observation: `roadmap_phase_fit` from 0 (no fit) to 3 (current phase), and `community_demand`, a count of distinct public requests.
- If all five are equal, the lower item number sorts first, so the order is never undefined. `assay rank` and `engine.Compare` apply this order.

### 7. Make "One job" something a machine can decide

**Problem:** "describable in one active verb-first sentence" cannot be judged without a model unless the rule is made concrete.

**Proposal:** the observation carries `task_count` and `task_sentence`. The gate fails when `task_count` is 2 or more. Each profile keeps two closed word lists: verbs that may open the sentence (pass) and openers that show it is not verb-first, such as "a", "the", "helps" (fail). A sentence that opens with a word on neither list is not judged; the item is paused with `E_NEEDS_SUBTOOL` until someone adds the word to a list.

**Reason:** this is the fail-closed rule applied to language. It will pause more items at first, and each pause is cured by a one-word change with a fixture.

### 8. Decide all five gates, always

**Clarification:** the specification says the failure reason is logged publicly. Assay decides every gate even after one has failed, so a candidate that is screened out shows every reason it failed, not only the first.

### 9. Say what Assay does when Muster does not answer, or refuses

**Gap:** the table defines `E_TIMEOUT` as a call that "times out". It does not cover a hub that refuses the connection or answers with a refusal.

**Proposal:** Assay acts on the status and the code of a refusal, as Muster's contract lists them:

- No answer, a refusal with `E_TIMEOUT`, or a refused token (`401`, `403`): keep the finished report in the spool and try again, waiting 1 s, then doubling up to 60 s. While a report is waiting, Assay leases nothing new.
- A `409` (the lease is not held, or the move is not allowed), or any other refusal with `E_PARSE`: the report can never succeed, so it is let go. Muster still holds the item, and the same item always scores the same.
- A `422` (Muster does not know contract v1): nothing will work until one side is redeployed, so Assay keeps its work and waits.
- A lease reply whose `lease` cannot be read cannot be answered at all. Assay logs it and waits; Muster offers the item again when the lease runs out.

### 10. Let the network policy resolve Muster's name

**Problem:** the policy is to permit "only ServiceAccount HTTP to Muster". Taken literally, the pod could not look up Muster's address.

**Proposal:** the policy allows two things out: TCP to the Muster pods in the `clearedstack` namespace, and DNS (port 53) to the cluster's DNS pods. Nothing is allowed in.

### 11. Name what the specification leaves unnamed

These are needed to deploy and are not given anywhere. Each is marked provisional in `deploy/assay.yaml`:

- Muster's service address and port (assumed `http://muster.clearedstack.svc.cluster.local`, port 80).
- The labels that select Muster's pods (assumed `app.kubernetes.io/name: muster`).
- The Kubernetes name of the "Standard (Workers)" priority class (left out).

### 12. Decide the rating thresholds

**Gap:** the specification fixes the gates, the seven weights and the recommendation bands, and those are implemented exactly. It does not say what evidence earns each rating from 1 to 5, in any category.

**Proposal:** none on the numbers. The thresholds and word lists now in the five profiles are placeholders, made up so that the engine can be run and tested. The project owner should set them before any score is published. Every change to one will show up as a change to a golden file.

### 13. Small points, recorded and not changed

- The overview says Assay replaces "Stages 4 (Screen) and 5 (Score)", while the registry calls it stage 03, formerly `cs-03-evaluate`. Assay uses the registry.
- The spool is an `emptyDir`: it outlives a container restart and a Muster restart, which is what the specification asks for, but not the deletion of the pod. Nothing is lost in that case, because Muster still holds the item and the same item always scores the same.
- The rules are YAML, read by a small strict reader written in this repository (`internal/yamlite`), so that Assay depends on nothing outside the Go standard library.
