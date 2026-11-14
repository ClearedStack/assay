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
     - *Public source*: Rules, formulas, and thresholds must come from officially released public materials (statutes, FAR, DFARS, NIST SPs).
     - *Runs in the browser*: Must execute entirely client-side (HTML/CSS/JS/WASM) with no backend server or remote API.
     - *Private by design*: Must work offline, require no accounts, emit zero tracking, and enforce `connect-src 'self'`.
     - *One job*: Must perform a single focused task describable in one active verb-first sentence.
     - *Arithmetic, not advice*: Must compute numbers, structure data, or look up citations, and never render legal advice.
  2. **100-point rubric:** Candidates clearing all five gates are rated `1`–`5` across seven weighted factors (composite `20`–`100`): Need for small businesses (`×4`, 20 pts), Gap (`×4`, 20 pts), Strength of public source (`×3`, 15 pts), Fits in a browser (`×3`, 15 pts), Small enough to read (`×2`, 10 pts), Can be checked (`×2`, 10 pts), and Low upkeep of reference data (`×2`, 10 pts).
  3. **Preliminary recommendation & split/merge flag:** Assigns `Build` (Score ≥ 75 and Gap > 1), `Link` (Score ≥ 75 and Gap = 1), `Park` (Score 55–74), or `Reject` (Score < 55). Flags **split/merge review** when Need is `4`–`5` and Small enough to read is `1`–`2`.
  4. **Five deterministic tie-breakers:** Roadmap phase fit → Gap severity → Low upkeep → Small code footprint → Community demand.
- **Outbound transitions:**
  - `screened_out`: Candidate failed one or more knockout gates (failure reason logged publicly).
  - `scored`: Candidate cleared all five gates and received its rubric breakdown and recommendation.
  - `needs_subtool`: Fail-closed pause (`E_NEEDS_SUBTOOL`) when an item's category lacks a matching rubric profile.

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
> One line above this heading has been edited: the state `screened-out` is now spelled `screened_out`, to match Muster's contract v1, which is the one place state names are defined, and em dashes in the bullet lists were replaced with colons.

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

**Proposal:** Assay scores `results.sextant` in the lease, aligned with Sextant's clean-room confidence-tier envelope:

| Field | Type | Rule |
| --- | --- | --- |
| `category` | string | Required lower-case slug, for example `pricing`. Selects the rubric profile. |
| `sub_tool` | string (optional) | Sextant extractor slug (`^[a-z][a-z0-9-]*$`, up to 64 characters), recorded for provenance. |
| `observed_on` | string (optional) | Calendar date `YYYY-MM-DD` when Sextant observed the candidate. |
| `facts` | object | Map of fact slug to `{"value": <bool\|int\|string\|[string]\|null>, "tier": "Seen"\|"Designed"\|"Stated"\|"Unknown"}`. |

Every fact envelope must carry `value` and `tier` and no other keys, and `value` is `null` if and only if `tier` is `"Unknown"` (violations are refused with `E_PARSE`). Which facts an observation must carry is declared by the category's profile (`tools/rubric-profiles/<category>/profile.yaml`, under `facts`), with a closed type and bounds for each; free-text string types are forbidden by the profile loader. By default, value checks require `tier` in `[Seen, Stated]`, plus `Designed` on a fact that the profile declares with `designed: true`, so an `Unknown` fact fails closed to `E_NEEDS_SUBTOOL` unless a rule explicitly matches `tier_in: [Unknown]`. In `private_by_design`, `emits_tracking: false` and `connect_src_self: true` explicitly require `tier_in: [Seen, Designed]` so unverified vendor self-claims (`Stated`) pause for inspection rather than passing the privacy gate. `Designed` is a value fixed by the ClearedStack template the tool will be generated from, proved after the build by Shakedown (see change 14). Results left by other workers, and `found`, are ignored.

**Reason:** closed sets, whole numbers and Sextant's `{value, tier}` invariant keep outcomes identical on every machine while preserving clean-room isolation (no free text from observed pages ever enters Assay) and distinguishing verified network/CSP inspection (`Seen`) and template guarantees (`Designed`) from self-asserted claims (`Stated`).

**Remaining handoff for Sextant.** Sextant's initial build emits tiered envelopes with `sub_tool` and `observed_on`, which Assay now accepts directly, and needs to emit `category` and the category's declared `facts` map. Until Sextant adds `category` and `facts`, observations without `category` continue to pause cleanly with `E_NEEDS_SUBTOOL`.

### 3. Define the outcome Assay hands back

**Gap:** the specification lists what is returned (gate results, a score, a recommendation) but not its shape.

**Resolution:** Logbook (`core.evaluation`), Muster (`tx.Evaluation`) and Countersign define the evaluation fields, and Assay emits that exact shape inside `result`:

- `profile` (the rubric profile slug, such as `pricing`),
- `gates` (an object mapping each gate slug to `{"passed": bool, "reason": string}`, with no other keys),
- for `scored` outcomes: `factors` (an object mapping each factor slug to `{"rating": 1..5, "weight": int}`, with no other keys), `score` (`20..100`), `recommendation` (`build`, `link`, `park`, or `reject`, lower-case as required by `core.recommendations()`), and `split_flag` (`bool`),
- plus `contract`, `worker` (`assay`), `id`, `state`, `category`, `rules` (the rubric version, profile name and version, and 1-based deciding rule rows under `rules.gates` and `rules.factors`), and `tie_breakers` (five entries: `id`, `name`, `value`).

Keeping the 1-based rule row numbers in `rules.gates` and `rules.factors` preserves full rule traceability while keeping `gates` and `factors` strictly valid for Logbook's `core.check_evaluation`. A candidate that is screened out carries all five gate verdicts and omits `factors`, `score`, `recommendation`, `split_flag` and `tie_breakers`. The golden files under `tools/rubric-profiles/` are the full examples.

### 4. Say which error an unknown field is

**Contradiction:** the failure table gives an "unknown field" `E_NEEDS_SUBTOOL`, but an unknown field also "fails contract schema validation", which the next row gives `E_PARSE`.

**Proposal:** broken data is `E_PARSE`: not JSON, a repeated key, a missing or mistyped field, a tier/null mismatch, a number out of range, a lease for anything but an observed item. Well-formed data that is merely unfamiliar is `E_NEEDS_SUBTOOL`: an observation that names no category or a category with no profile, a field or fact nobody declared, a value outside a declared set, an `Unknown` tier or `Stated` privacy claim where `Seen` or `Designed` is required, or a case no rule row covers. When a lease is both, `E_PARSE` is reported.

**Reason:** this matches the one-line meanings of the codes ("the data was broken" against "the data was valid and unfamiliar"), and an item can only be called unfamiliar once it is known to be valid. It also matters in practice: Muster holds an `E_PARSE` item until the owner releases it, but replays a paused item by itself when a new build of Assay is deployed.

### 5. Accept that an error's detail does not reach Bellbuoy

**Gap:** the failure table says an `E_PARSE` item is reported "via Bellbuoy" with a fingerprint. Muster's `fail` and `needs_subtool` calls carry no field for one, and Muster gives Bellbuoy an incident step that names only the item, the worker, the code and the state.

**Proposal:** Assay writes the reason, the place and the fingerprint of every error to its own log, as one JSON line per item, and sends Muster only what the contract defines. If Bellbuoy needs the fingerprint to deduplicate tasks, that is a change to Muster's contract, not something Assay can add.

### 6. Say what the tie-breakers read and how they sort

**Gap:** the five tie-breakers are named but not defined. Two of them (roadmap phase fit, community demand) are not rubric factors, and Assay sees one item at a time, so it cannot itself put candidates in order.

**Proposal:**

- Each scored outcome carries the five values, in order. Anything that ranks candidates sorts by `score`, then by each value in turn, higher first.
- Gap severity is the Gap rating, Low upkeep is the Low-upkeep rating, and Small code footprint is the Small-enough-to-read rating.
- Roadmap phase fit and Community demand are whole-number facts in the observation: `roadmap_phase_fit` from 0 (no fit) to 3 (current phase), and `community_demand`, a count of distinct public requests (`Seen`, `Designed` or `Stated`; an `Unknown` tier pauses the item with `E_NEEDS_SUBTOOL`).
- If all five are equal, the lower item number sorts first, so the order is never undefined. `assay rank` and `engine.Compare` apply this order.

### 7. Make "One job" something a machine can decide without free text

**Problem:** "describable in one active verb-first sentence" cannot be judged directly without either a model or passing free-text sentences from vendor pages across the clean-room boundary.

**Proposal:** the observation carries `task_count` (`1..100`) and `core_job` (`type: enum`). Each `version: 4` profile declares a closed vocabulary of focused single-job slugs covering every planned tool archetype in its ClearedStack catalog category plus all four base `CoreJobs` in Sextant (`date_calculation`, `amount_calculation`, `eligibility_check`, `record_search`), alongside broad-scope disqualifiers (`multi_workflow`, `portal_suite`, `open_ended_advisory`):

- `pricing`: `rate_calculation`, `amount_calculation`, `cost_allocation`, `price_escalation`, `price_table_lookup`, `interest_calculation`, `date_calculation`, `eligibility_check`, `record_search`.
- `proposal`: `page_budget_check`, `section_count`, `compliance_matrix`, `format_check`, `requirement_extraction`, `acronym_table_check`, `date_calculation`, `amount_calculation`, `eligibility_check`, `record_search`.
- `cyber`: `control_mapping`, `score_calculation`, `requirement_tally`, `citation_lookup`, `marking_generation`, `schema_validation`, `date_calculation`, `amount_calculation`, `eligibility_check`, `record_search`.
- `market`: `record_search`, `set_aside_filter`, `size_standard_check`, `code_lookup`, `clause_crosswalk`, `date_calculation`, `amount_calculation`, `eligibility_check`.
- `devsecops`: `config_validation`, `manifest_lint`, `format_conversion`, `policy_check`, `schema_diff`, `certificate_check`, `checksum_verification`, `date_calculation`, `amount_calculation`, `eligibility_check`, `record_search`.

`one_job` fails when `task_count >= 2` or `core_job` is in `[multi_workflow, portal_suite, open_ended_advisory]`, passes when `task_count == 1` and `core_job` is in the profile's single-job enum (`Seen`, `Designed` or `Stated`), and pauses with `E_NEEDS_SUBTOOL` if `core_job` is `Unknown` or outside the declared enum.

**Reason:** replacing free-text `task_sentence` with a closed `core_job` enum enforces Sextant's clean-room guarantee by construction while keeping `one_job` deterministic and fail-closed.

### 8. Decide all five gates, always

**Clarification:** the specification says the failure reason is logged publicly. Assay decides every gate even after one has failed, so a candidate that is screened out shows every reason it failed, not only the first.

### 9. Say what Assay does when Muster does not answer, or refuses

**Gap:** the table defines `E_TIMEOUT` as a call that "times out". It does not cover a hub that refuses the connection or answers with a refusal.

**Proposal:** Assay acts on the status and the code of a refusal, as Muster's contract lists them:

- No answer, a refusal with `E_TIMEOUT`, or a refused token (`401`, `403`): keep the finished report in the spool and try again, waiting 1 s, then doubling up to 60 s. While a report is waiting, Assay leases nothing new.
- A `409` (the lease is not held, or the move is not allowed), or any other refusal with `E_PARSE` (including `400` `bad_field` from Logbook): the report can never succeed, so it is let go. Muster still holds the item, and the same item always scores the same.
- A `422` (`E_NEEDS_SUBTOOL`: Muster does not know contract v1, or Logbook has no rule for a value): nothing will work until one side is redeployed, so Assay keeps its work and waits.
- A lease reply whose `lease` cannot be read cannot be answered at all. Assay logs it and waits; Muster offers the item again when the lease runs out.

### 10. Let the network policy resolve Muster's name

**Problem:** the policy is to permit "only ServiceAccount HTTP to Muster". Taken literally, the pod could not look up Muster's address.

**Proposal:** the policy allows two things out: TCP port `8080` to the Muster pods (`app.kubernetes.io/name: muster`) in the `clearedstack` namespace, and DNS (UDP and TCP port 53) to `kube-system`. Nothing is allowed in.

### 11. Name what the specification leaves unnamed

`deploy/assay.yaml` names the container image `assay` only (the deployer gives the registry and `sha256` digest with a kustomize image override), `.ko.yaml` pins the Chainguard static base image by `sha256` digest for `linux/arm64` builds, and the pod schedules onto `arm64` nodes excluding `cm3588-plus`, setting:

- Muster's service address and port (`http://muster.clearedstack.svc.cluster.local:8080`, matching Muster's `:8080` listener).
- The labels that select Muster's pods (`app.kubernetes.io/name: muster` in `clearedstack`).
- The Kubernetes name of the "Standard (Workers)" priority class is still left out until the cluster defines it.

### 12. Calibrate the rating thresholds across all five profiles

**Gap:** the specification fixes the gates, the seven weights and the recommendation bands, and those are implemented exactly. It does not say what evidence earns each rating from 1 to 5, in any category.

**Resolution (`version: 3` profiles, unchanged in `version: 4`):** all five profiles (`pricing`, `proposal`, `cyber`, `market`, `devsecops`) use calibrated ClearedStack v3.1 bands:

- **`need` (`need_signals`):** `>=30` / `15..29` / `7..14` / `2..6` / `0..1` in `pricing` and `cyber`; `>=25` / `12..24` / `5..11` / `2..4` / `0..1` in `proposal`, `market`, and `devsecops`.
- **`gap` (`best_alternative`):** `none` (5), `paid_only` (4), `outdated` (3), `partial` (2), `complete` (1).
- **`public_source_strength` (`source_kind` + `source_precision`):** category-specific authority hierarchy placing statutory/FAR/DFARS/NIST SP exact formulas and lookup tables at `5`/`4`, defined procedures at `3`, agency guidance procedures at `2`, and general principles at `1`.
- **`browser_fit` (`largest_input_kb` plus category payload modifiers):** `<=64` / `65..512` / `513..4096` / `4097..16384` / `>=16385` KB in `pricing` and `cyber`, `<=128` / `129..1024` (JS vs WASM) / `1025..8192` / `>=8193` KB in `devsecops`, `<=1024` / `1025..8192` / `8193..32768` / `32769..131072` / `>=131073` KB in `proposal`, and `<=256` KB with `<=25k` bundled rows down to `>=2049` KB or `>1000k` rows in `market`.
- **`readable_size` (`est_source_lines` plus complexity modifiers):** `<=300` (5), `301..750` (4), `751..1500` (3), `1501..2500` (2), `>=2501` (1), with markdowns above `control_count > 110` (NIST SP 800-171 Rev 2 scale) in `cyber` and `config_formats >= 3` in `devsecops`, ensuring high-need candidates above `1500` SLOC trigger `split_flag: true`.
- **`checkable` (`worked_examples`):** `>=10` (5), `5..9` (4), `2..4` (3), `1` (2), `0` (1), matching Waybill's nominal-plus-boundary test-vector model at `>=2`.
- **`low_upkeep` (`reference_updates_per_year` plus `rate_tables` / `template_count`):** `0` static (5), `1` annual (4), `2..4` quarterly FAC/table cycles (3), `5..12` monthly bulletins (2), `>=13` sub-monthly churn (1), marked down one band when `rate_tables >= 3` or `template_count >= 3`.

Every change to any threshold is captured in the golden fixture diffs.

### 13. Small points, recorded and not changed

- The overview says Assay replaces "Stages 4 (Screen) and 5 (Score)", while the registry calls it stage 03, formerly `cs-03-evaluate`. Assay uses the registry.
- The spool is an `emptyDir`: it outlives a container restart and a Muster restart, which is what the specification asks for, but not the deletion of the pod. Nothing is lost in that case, because Muster still holds the item and the same item always scores the same.
- The rules are YAML, read by a small strict reader written in this repository (`internal/yamlite`), so that Assay depends on nothing outside the Go standard library.

### 14. Accept the Designed tier: a fact the build template guarantees

**Gap:** most facts in a profile describe the tool ClearedStack would build, not the vendor page Sextant read: `client_runtime`, `needs_backend`, `works_offline`, `needs_account`, `emits_tracking`, `connect_src_self` and others. A vendor page cannot show them, and a tool that does not exist yet cannot have been inspected. The `private_by_design` gate required tier `Seen` on `emits_tracking` and `connect_src_self`, so no candidate could pass it before it was built.

**Resolution (`version: 4` profiles):** a fourth tier, `Designed`, meaning "fixed by the ClearedStack template the tool will be generated from (strict CSP `connect-src 'self'`, offline, no account), proved after the build by Shakedown's listing conditions".

- An envelope with tier `Designed` carries a non-null value, like `Seen` and `Stated`. A null value is refused with `E_PARSE`. The tier vocabulary is closed: `Seen`, `Designed`, `Stated`, `Unknown`, and nothing else.
- Only a fact declared with `designed: true` under `facts` may carry `Designed`. Each profile declares eleven, the facts about the tool still to be built: `client_runtime`, `connect_src_self`, `emits_tracking`, `est_source_lines`, `largest_input_kb`, `needs_account`, `needs_backend`, `needs_remote_api`, `output_kinds`, `task_count`, `works_offline`. A condition on such a fact with no `tier_in` accepts `Seen`, `Designed` and `Stated`; on any other fact it accepts `Seen` and `Stated`. `Unknown` still fails closed.
- `Designed` on any other fact (for example `need_signals`, `core_job`, `source_public`) pauses the item with `E_NEEDS_SUBTOOL` `no_matching_rule`, the same answer as any tier a rule does not accept. A `tier_in` that names `Designed` on such a fact is refused when the profile loads.
- In all five profiles the passing `private_by_design` row takes `tier_in: [Seen, Designed]` on `emits_tracking` and `connect_src_self`. `Stated` still does not pass: a vendor's own claim pauses the item with `E_NEEDS_SUBTOOL`.
- Each changed profile's `version` goes from 3 to 4, with the reason in its header comment. Every saved outcome records `profile_version`, so the golden files change in that one field.

**Reason:** the privacy rule's intent stays as it was, that an unverified claim must not pass. The tier says who stands behind the value. Sextant writes `Designed` only for facts the template fixes, Assay refuses it anywhere else, and Shakedown proves them against the built tool.
