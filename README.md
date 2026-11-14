<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/logo-dark.svg">
    <img src="brand/logo-light.svg" alt="Trawl" height="56">
  </picture>
</p>

<p align="center">
  <strong>Finds candidate tools and records each one exactly once.</strong><br>
  Part of <a href="https://clearedstack.com">ClearedStack</a>
</p>

---

> **Status: ready to deploy against Muster's contract v1 (awaiting cluster deployment order).** Trawl builds with standard library Go only, passes `go vet ./...` and `go test -race ./...`, replays all 52 saved examples from Muster's [contract v1](https://github.com/ClearedStack/muster/blob/claude/repo-build-prompt-1csgx6/contract/v1/CONTRACT.md), and includes hardened Kubernetes manifests in [`deploy/`](deploy) for namespace `cs-workers`. See [Gaps between contract v1, Muster/Logbook and Trawl](#gaps-between-contract-v1-musterlogbook-and-trawl) and [What is not done](#what-is-not-done).

Trawl is stage 01 of the ClearedStack pipeline. ClearedStack is an open pipeline that finds ideas for government-contracting tools, screens and scores them, and builds each one from the public rule. Trawl is meant to be understood and used without the rest.

## What works today

Trawl has two duties, and `trawl work` does both.

- **Find.** On a schedule it reads the public sources listed in a sources file, runs the sub-tool for each, cleans every address it finds, and files each one with Muster. Muster makes a `discovered` item for an address it has not seen, and answers "known" for one it has.
- **Record.** It leases its `discovered` items from Muster one at a time, checks what was recorded, and completes each to `recorded`, which hands it to Sextant, or to `rejected`.

Around those:

- **One address in, one canonical address out.** Sixteen rules, each with a file of saved examples, enforcing the same 2,000-byte limit as Muster and Logbook. `trawl normalize` runs them on its own, with no hub.
- **Each address is recorded once.** Muster compares addresses exactly as sent, so making equivalent addresses equal is Trawl's job: it only ever files the clean form.
- **Four sub-tools**, one per source: `sam-gov`, `github-issues`, `web-sweep` and `site-form`.
- **A spool.** Everything bound for Muster is written to disk first. If Muster is away, the work waits and is sent in order when it returns, even if Trawl itself restarted in between.
- **Three error codes and nothing else.** `E_TIMEOUT`, `E_PARSE` and `E_NEEDS_SUBTOOL`, each with a reason from a fixed list and a 12-character fingerprint. No error or log line carries an address, a machine name or a credential.
- **No AI and no clock in the output.** The same sources and items in the same order give the same bytes. A test runs the whole pipeline twice and compares.
- **Small, and standard library only.** There are no dependencies outside Go itself. The built binary recorded 2,001 made-up items with a peak of about 10 MiB of memory, against a budget of 30 MiB.

## Run it

You need Go 1.24 or later.

```sh
go build -o trawl ./cmd/trawl

# Clean, hash and remember addresses, with no hub. The store is a text file.
printf 'HTTPS://Tools.Agency-Example.GOV/wage/index.html?utm_source=news#top\n' \
  | ./trawl normalize -store trawl.store
# url     https://tools.agency-example.gov/wage
# sha256  4f224a35258b04c87bebd7a0ab0edd93a83c8ccb7b9434e23fb9970f355f640d
# seen    no
# id      PA-0001

# Run the worker against Muster.
./trawl work -muster-url http://HUB-ADDRESS -sources sources.json -spool /var/spool/cs
```

`trawl work` takes these flags (each flag overrides its environment variable when both are set):

| Flag | Environment | Default | Meaning |
| --- | --- | --- | --- |
| `-muster-url` | `MUSTER_URL` | none, required | Address of Muster. |
| `-token-file` | `TRAWL_TOKEN_FILE` | the Kubernetes ServiceAccount token path | File holding the token sent to Muster. It is read again for every call. Empty sends none. |
| `-spool` | `TRAWL_SPOOL_DIR` | `/var/spool/cs` | The one directory Trawl writes to. |
| `-sources` | `TRAWL_SOURCES` | none | The sources file. Without it Trawl sweeps nothing and only records. |
| `-sweep-every` | `TRAWL_SWEEP_EVERY` | `6h` | Time between sweeps. `0` sweeps once. |
| `-drain` | none | off | Sweep once, then stop when there is nothing to lease and the spool is empty. |

If `CONTRACT_VERSION` is set in the environment and is not `v1`, `trawl work` refuses to start (exit status 2). If `GOMEMLIMIT` is not set in the environment, `trawl work` sets a soft Go runtime limit of 24 MiB inside its 30 MiB budget.

The sources file is JSON. Each entry names a source Muster knows, the format of the document, and the public address to read it from:

```json
{
  "sources": [
    {"source": "web-sweep", "format": "web-sweep.page.v1", "fetch": "https://www.agency-example.gov/small-business/resources"},
    {"source": "github-issues", "format": "github-issues.v1", "fetch": "https://code.agency-example.gov/api/issues"},
    {"source": "sam-gov", "format": "sam-gov.opportunities.v2", "fetch": "https://feed.agency-example.gov/opportunities/v2/search?limit=100"}
  ]
}
```

### Test-only setting

`TRAWL_LOCAL_TEST_ORIGIN` lets an end-to-end test record an item served from the test machine. It is for tests only, and `deploy/` never sets it. Its value must be exactly `http://127.0.0.1:<port>` or `http://[::1]:<port>`, with a port from 1 to 65535, no path, no trailing slash and no user name. Any other non-empty value makes `trawl normalize` and `trawl work` refuse to start (exit status 2).

When it is set, an address on exactly that origin (scheme `http`, the same host and the same port) passes the public-host rule and is kept as `http` with its host and port as given. The other rules, including the path rules, apply as usual. Every other loopback, private, IP-literal or bracketed address, such as `http://127.0.0.1:<other port>` or `http://localhost:<port>`, is refused as before. Both commands print one fixed JSON warning line to standard error at start. Trawl still never fetches such an address: the fetcher keeps refusing loopback and private hosts. Unset, output is unchanged.

```sh
printf 'http://127.0.0.1:41234/calculator\n' | TRAWL_LOCAL_TEST_ORIGIN=http://127.0.0.1:41234 ./trawl normalize
```

### Tests

```sh
go vet ./...
go test -race ./...    # everything, including the 52 contract examples and binary budget check
go test -short ./...   # skips the test that builds and runs the binary
```

The saved examples are the `testdata` folders. To change behaviour on purpose, change the code, run `TRAWL_UPDATE_GOLDEN=1 go test ./...` to rewrite the expected files, and review the difference like any other change. Without that variable, any change in output fails a test.

### Container image and Kubernetes manifests

The image is built from source with [ko](https://ko.build); there is no Dockerfile. Settings are in [`.ko.yaml`](.ko.yaml), with `cgr.dev/chainguard/static` pinned by SHA-256 digest and reproducible `-trimpath` / `-buildid=` flags.

```sh
KO_DOCKER_REPO=lazarette.clearedstack.svc.cluster.local:5000 \
  ko build --platform=linux/arm64 --base-import-paths ./cmd/trawl
```

Kubernetes manifests for namespace `cs-workers` live in [`deploy/`](deploy): [`namespace.yaml`](deploy/namespace.yaml), [`trawl.yaml`](deploy/trawl.yaml) (`ServiceAccount`, `ConfigMap` and `Deployment` running as non-root UID 65532 with a read-only root filesystem, all capabilities dropped, `arm64` / `NotIn cm3588-plus` node affinity, and 16 MiB / 30 MiB memory request/limit), and [`networkpolicy.yaml`](deploy/networkpolicy.yaml) (`default-deny` plus DNS, Muster and public HTTP/HTTPS egress only).

## How Trawl talks to Muster

Every call is a `POST` of a JSON object that carries `"contract": "v1"`, with Trawl's ServiceAccount token as a bearer token. Muster refuses a body with a field the call does not define, so Trawl sends exactly these and nothing else:

| Call | Trawl sends | Trawl reads |
| --- | --- | --- |
| `/v1/items` | `contract`, `source`, `title`, `address`, `result` | `201` is new, `200` with `"duplicate": true` is known, and `id` |
| `/v1/lease` | `contract` | `lease`, `kind`, `id`, `state`, `found` and `results`; `204` when there is nothing |
| `/v1/heartbeat` | `contract`, `lease` | the status |
| `/v1/complete` | `contract`, `lease`, `state`, `result` | the status |
| `/v1/fail` | `contract`, `lease`, `code` | the status |
| `/v1/needs_subtool` | `contract`, `lease` | the status |

Muster decides the order of leases: `urgent` first, then `normal`, then `backfill`. Trawl takes what it is offered. Muster also mints the `PA-XXXX` identifiers and owns the rate limits.

What Trawl files as `result`, and reports again when it completes an item to `recorded`, is the record the next worker reads under `results.trawl`:

```json
{"source": "web-sweep", "address": "https://tools.agency-example.gov/wage", "title": "Wage calculator", "kind": "link", "via": "https://www.agency-example.gov/small-business/resources"}
```

A refusal is sorted by its status and `cause`. `409` with `lease_not_held` means the lease ran out while Trawl held it, so the stale staged outcome is dropped. `409` with `move_not_allowed` on `needs_subtool` falls back to `fail` (`E_PARSE`), while any other `move_not_allowed` or permanent refusal (`400`, `404`, `405`, `413`, `415`, `422`) cannot succeed if repeated and is set aside in the spool and logged. `401`, `403`, `429` and `5xx` mean try later, and the work stays in the spool. Only the status, the code and Muster's `cause` word are logged, never its sentence.

### What Trawl does with a leased item

| The item | Outcome |
| --- | --- |
| Its address is clean as recorded | Completed to `recorded`. |
| Its address is not public or is broken | Completed to `rejected`, with the public error as the result. |
| Cleaning changes its address, and its source is one Trawl sweeps | Trawl files the clean address. If it is new, it becomes an item of its own and this one is `rejected` as `superseded`. If it is known, this one is `rejected` as `duplicate`. |
| Cleaning changes its address, and it came through the site's form | Completed to `recorded` with the clean address in the result. See the first gap below. |
| What was recorded for the form is not the expected shape | `fail` with `E_PARSE`. |
| No rule covers it: an unknown source, a host name outside ASCII, a step that is not a discovered item | `needs_subtool`. Contract v1 refuses that for a `discovered` item (`409 move_not_allowed`), so Trawl then fails it with `E_PARSE`, which holds it for the owner. See the second gap below. |

## In this repository

| Path | What it is |
| --- | --- |
| [`SPEC.md`](SPEC.md) | The specification: what Trawl does, its interfaces, how it fails and how it is kept safe. |
| [`PROMPT.md`](PROMPT.md) | The prompt this build was started with. |
| [`component.json`](component.json) | This component's entry in the ClearedStack naming registry. |
| [`deploy/`](deploy) | Hardened Kubernetes manifests (`Namespace`, `ServiceAccount`, `ConfigMap`, `Deployment` and `NetworkPolicy`) for `cs-workers`. |
| [`.ko.yaml`](.ko.yaml) | Reproducible `ko` container build settings with the Chainguard `static` base image pinned by digest. |
| [`cmd/trawl`](cmd/trawl) | The `trawl` command: `normalize`, `work`, `version`. |
| [`tools/`](tools) | The sub-tools. [`registry.yaml`](tools/registry.yaml) lists them; each folder holds `match.yaml`, the Go logic and `testdata/`. |
| [`internal/normalize`](internal/normalize) | The address rules and their saved examples. |
| [`internal/muster`](internal/muster) | The interface to Muster, the HTTP client and a fake hub for tests. |
| [`testdata/contract`](testdata/contract) | A copy of all 52 Muster contract v1 examples, replayed by the tests. |
| [`internal/spool`](internal/spool) | The on-disk queue of work bound for Muster. |
| [`internal/worker`](internal/worker) | The sweep, file, lease and record loop. |
| [`internal/fetch`](internal/fetch), [`internal/robots`](internal/robots) | Reading public addresses, and obeying `robots.txt`. |
| [`internal/intake`](internal/intake), [`internal/screen`](internal/screen) | Rules every candidate passes: marking screen, markup stripping, normalization. |
| [`internal/tool`](internal/tool) | What a sub-tool is, and the reader for the two YAML files. |
| [`internal/errs`](internal/errs) | The three public error codes. |
| [`internal/dedupe`](internal/dedupe), [`internal/store`](internal/store) | The hash and the plain file store behind `trawl normalize`. The worker uses neither. |
| [`brand/`](brand) | The Trawl logo and mark, for light and dark backgrounds. |

## At a glance

| | |
| --- | --- |
| **Stage** | 01 of 10 in the pipeline |
| **Formerly** | `cs-01-intake` |
| **Namespace** | `cs-workers` |
| **Container image** | `lazarette.clearedstack.svc.cluster.local:5000/trawl` |
| **Memory budget** | 30 MiB |
| **Depends on** | [Muster](https://github.com/ClearedStack/muster) |
| **Depended on by** | Nothing else in the platform |
| **Rules for particular cases** | `sam-gov`, `github-issues`, `web-sweep`, `site-form` |

### Boundaries

- Leases work from Muster over cluster-private HTTP
- Reads public sources: SAM.gov notices, GitHub issues, scheduled site sweeps and the site's own form

## How an address is normalized

The rules run in this order. Each has a file of saved examples in [`internal/normalize/testdata`](internal/normalize/testdata), 118 examples in all.

| Rule | What it does |
| --- | --- |
| `surrounding-whitespace` | Drops spaces, tabs and line ends around the address. |
| `syntax` | Refuses an address that is empty, longer than 2,000 bytes (checked both before and after percent-escaping, matching Muster and Logbook), has control characters, a backslash, no scheme, no host, a bad port or a bad percent escape. |
| `scheme-lowercase` | Lowercases the scheme. |
| `public-scheme` | Accepts only `http` and `https`. `http` is kept as `http`. |
| `host-lowercase` | Lowercases the host name. `www.` is kept. |
| `host-trailing-dot` | Drops one trailing dot from the host name. |
| `public-host` | Refuses user names, IP addresses, single-label names and reserved or private suffixes such as `.local`, `.internal` and `.svc`. |
| `default-port` | Drops port 80 for `http` and 443 for `https`, an empty port, and leading zeros. |
| `fragment` | Drops the `#` fragment. |
| `percent-encoding` | Writes percent escapes in upper case and escapes bytes outside printable ASCII and the characters `" < > ^ { \| }` and the backtick. |
| `session-params` | Drops `jsessionid`, `phpsessid`, `sessionid`, `session_id` and `sessid` from the query, and `;jsessionid` from the path. |
| `tracking-params` | Drops `utm_*`, `ref`, `fbclid` and `gclid` from the query. |
| `query-order` | Drops empty query pairs and sorts the rest by key. |
| `default-index` | Drops `index.html`, `index.htm`, `index.shtml`, `index.php`, `index.asp`, `index.aspx`, `index.jsp`, `default.html`, `default.htm`, `default.asp` or `default.aspx` at the end of the path. |
| `trailing-slash` | Drops slashes at the end of the path. |
| `root-path` | Writes an empty path as `/`. |

## The sub-tools

Each sub-tool is a folder under `tools/` with a matching rule, logic and fixtures, listed in `tools/registry.yaml`. A sources-file entry is matched to a sub-tool by its `source` and `format`, and a leased item by its `source`. Exactly one rule must match.

| Sub-tool | Reads | What it yields | Saved cases |
| --- | --- | --- | --- |
| [`sam-gov`](tools/sam-gov) | A fetched page of notices, format `sam-gov.opportunities.v2` | From each notice: the additional-information link, each attachment, and each address in the description text. An address containing "calculator" is marked as one. | 16 |
| [`github-issues`](tools/github-issues) | A fetched list of issues, format `github-issues.v1` | From each issue labelled `tool-suggestion` or `paperwork-suggestion` and written with the matching issue form: the address and the name. | 19 |
| [`web-sweep`](tools/web-sweep) | A fetched HTML page, format `web-sweep.page.v1` | Each link whose text or path contains a word from a fixed list, such as "calculator", "template" or "checklist". | 10 |
| [`site-form`](tools/site-form) | What Muster recorded for a candidate from the site's form: `source`, `title`, `address` | That one candidate, if the record has exactly that shape and is at most 4 KB. | 20 |

Adding a sub-tool means adding a folder with those three parts, a line in `tools/registry.yaml` and a line in `tools/tools.go`. A test fails if a folder is not registered or a registered folder lacks a part.

The two YAML files are read by a small reader in `internal/tool`, written against the standard library. It accepts plain `key: value` lines, nested keys and lists of plain words, and refuses everything else.

## Errors

| Code | Meaning |
| --- | --- |
| `E_TIMEOUT` | Something outside took too long, was unreachable or answered with a server error. A source that fails this way is read again on the next sweep. |
| `E_PARSE` | The data was broken or failed validation. |
| `E_NEEDS_SUBTOOL` | The data was valid and no rule covers it. |

An error is printed as, for example, `E_PARSE tool=site-form reason=schema fp=6d5c8ff7a60c`. The fingerprint of an `E_NEEDS_SUBTOOL` depends only on the kind of case, so every case stopped for the same reason reports the same one.

When one entry in a fetched document is broken, such as one notice in a page, that entry is refused and logged and the rest of the document goes on. When one entry is valid and unfamiliar, the whole document is set down until a rule exists. Reading it again later is safe: Muster answers "known" for what it already has.

## Requirements in `SPEC.md` and where each stands

### Interfaces and data contracts

| Requirement | State | Where it is tested |
| --- | --- | --- |
| `lease`, `heartbeat`, `complete`, `fail`, `needs_subtool` over HTTP with the ServiceAccount token, contract `v1` | Built to Muster's contract and replayed against its examples | `internal/muster` |
| Lanes drained `urgent`, then `normal`, then `backfill` | Muster's to order under contract v1. Trawl takes what it is offered. | `TestLaneOrderIsTheHubs` |
| Lowercase scheme and host | Built | `internal/normalize` |
| Strip tracking and session parameters and fragments | Built | `internal/normalize` |
| Remove trailing slashes and default index filenames | Built | `internal/normalize` |
| Public-only scheme and host | Built | `internal/normalize`, `internal/fetch` |
| SHA-256 of the canonical address, checked through Muster | Changed: Trawl sends the clean address and Muster keeps the key. `trawl normalize` still prints the SHA-256. | `TestNoHashIsSent` |
| Existing address is logged as a sighting, no duplicate | Built. Muster notes the sighting when it answers "known". | `TestSweepFilesAndRecords` |
| New address gets a `PA-XXXX` identifier, moves `discovered` to `recorded`, and is queued for Sextant | Built. Muster mints; Trawl completes to `recorded`. | `internal/worker`, `tools/testdata/pipeline.golden.json` |
| Work staged in `/var/spool/cs` before Muster is told, flushed in order | Built | `TestHubRestartMidBatch`, `TestOutcomeWaitsForTheHub` |

### Failure modes

| Failure | State | Where it is tested |
| --- | --- | --- |
| Unknown source format is stopped and the rest carries on | Built | `TestSweepFailuresDoNotStopTheRest`, `TestLeasedItems` |
| Paused items are replayed once a rule exists | Not possible for Trawl under contract v1. See the second gap below. | n/a |
| Upstream timeout does not lose anything | Built. The source is read again on the next sweep. | `TestSweepFailuresDoNotStopTheRest`, `internal/fetch` |
| Malformed payload halts that item with a sanitized fingerprint | Built | every sub-tool's `testdata`, `TestLeasedItems` |
| Hub unavailable: work is kept and flushed later | Built | `TestHubRestartMidBatch` |

### Security

| Rule | State | Where it is tested |
| --- | --- | --- |
| No database access; all state goes through Muster | Built. Trawl holds no database client or credential. | By construction |
| Form shield 1 to 3: proof of work, honeypot, rate limit | Muster's, checked before an item exists. Trawl is never given the visitor's address or the proof. | `TestSiteFormFilingByConn` |
| Form shield 4: 4 KB schema with markup stripped | Checked by Trawl's `site-form` | `tools/site-form`, `internal/screen` |
| Form shield 5: owner approval | After Trawl | n/a |
| Never ignores `robots.txt` | Built. Asked before every address and every redirect step. | `internal/fetch`, `internal/robots` |
| Never creates accounts, requests demos, or passes paywalls or CAPTCHAs | Built. Trawl sends `GET` only to public sites, with no cookies and no credentials, and stops at a sign-in. | `internal/fetch` |
| Never accepts controlled information | Partly. Text carrying a control marking is refused and not stored. | `internal/screen` |
| Runs unprivileged with a read-only filesystem | Built. The binary writes only under the spool directory, and `deploy/trawl.yaml` sets `runAsNonRoot` (UID 65532), `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false` and `drop: ["ALL"]`. | `cmd/trawl/deploy_test.go`, `TestWorkStaysWithinMemoryBudget` |

## Gaps between contract v1, Muster/Logbook and Trawl

These are for Muster, Logbook and the build/deploy components to settle. Trawl's behaviour today is stated for each.

1. **Muster's PostgreSQL store loses `source` and `results` across Muster restarts.** Logbook's `core.item` table stores `(item_id, state, title, address, address_sha256, recorded_at)` without `source` or per-worker `results` columns, and Muster's SQL store (`internal/logbook/sql/sql.go`) keeps those in an in-memory map (`beside`). If Muster restarts while `discovered` items wait in `core.queue`, Muster leases them with `found.source` empty and `results` nil; Trawl refuses to guess (`unfamiliar_item`) and holds the item via `fail` (`E_PARSE`). Logbook and Muster need to persist `source` and `results` in PostgreSQL 17.
2. **A form address cannot be filed again in its clean form.** Muster compares addresses exactly as sent and leaves cleaning to Trawl. For an item Conn filed with an address that cleaning changes, the only way to learn whether the clean address is known is to file it, and Muster refuses a `site-form` filing without the visitor's proof of work, which Trawl does not have. So Trawl records the item with the clean address in its result. The same address typed two ways through the form therefore becomes two recorded items. `TestFormAddressTypedTwoWays` pins this. Either of these would close it: let Trawl file a `site-form` address without `pow` and `visitor`, or add a call that asks whether an address is known.
3. **Trawl cannot pause an item.** The state table has no move from `discovered` to `needs_subtool`. When no rule covers a leased item, Trawl calls `needs_subtool`, is refused (`409 move_not_allowed`), and fails the item with `E_PARSE`, which holds it for the owner to release. That is the wrong code for data that is valid and unfamiliar, and such items are not replayed automatically when a new sub-tool ships.
4. **Nothing reports a failed sweep.** A source that times out or has no rule is logged with its public error and tried again on the next sweep. There is no leased item to fail or pause, so Muster is not told.
5. **A title is required.** Muster refuses a filing with no title (`<= 200` bytes). A candidate found without one, such as a link with no text, is filed under its own address as its title, cut at the last whole UTF-8 character within 200 bytes.

## What is not done

- **Pushing to Lazarette and running in-cluster.** `deploy/` holds the Kubernetes manifests for `cs-workers`, and `TestContractExamplesThroughClient` replays all 52 saved examples from Muster's `contract/v1/examples/` (`470dddf`). Before Shipkeeper applies `deploy/trawl.yaml`, Lazarette and Keelworks must build and push `lazarette.clearedstack.svc.cluster.local:5000/trawl` for `linux/arm64` with `ko` and replace the pre-push binary SHA-256 in `deploy/trawl.yaml` with the pushed OCI manifest digest, and Logbook, Muster, Assay and Countersign must be deployed ahead of Trawl in the fixed platform order.
- **Live SAM.gov.** Its search API needs a key and Trawl holds no credentials. A `sam-gov` source must be an address that needs no key.
- **Host names outside ASCII.** They are stopped as `E_NEEDS_SUBTOOL` (`idn_host`). Converting them correctly needs tables the Go standard library does not have.
- **Unmarked sensitive text.** Trawl refuses text with a control marking. It cannot recognise controlled information or bid figures that carry no marking. It limits the exposure by keeping only an address and a title of at most 120 characters.
- **Path clean-up beyond the specification.** `.` and `..` segments in an absolute address, doubled slashes inside a path, and percent escapes of ordinary letters are left as they are.
- **Pages that are not HTML or not UTF-8.** A fetched PDF is stopped as `not_html`.
- **A proxy.** The fetcher connects directly and ignores proxy settings.

## Choices where the specification was silent

| Question | Choice | Reason |
| --- | --- | --- |
| Who starts a sweep? | Trawl, from a sources file, every `-sweep-every`. | A lease in contract v1 carries an item, never a job to read a source. |
| What makes a link a candidate? | A whole word from a fixed list in its text or path. The list is `Words` in `tools/web-sweep/websweep.go`. | The specification gives no rule. This one is provisional and easy to change. |
| What does a suggestion issue look like? | Two labels and two issue forms, described at the top of `tools/github-issues/githubissues.go`. | The specification says "structured" without giving the structure. |
| What is a "sanitized fingerprint"? | The first 12 hex characters of a SHA-256 digest of the offending bytes, or of the kind of case for `E_NEEDS_SUBTOOL`. | Lets identical failures be grouped without showing the data. |
| A lease that ended while its outcome waited in the spool | The outcome is dropped and the item is decided again when Muster offers it. | Muster is the only authority on who holds an item. |
| Edit proposals from the site | Not Trawl's. They go from Conn to Muster and on to the owner. | Contract v1 routes them to Countersign. |

## Names

`trawl` is this component's identifier everywhere: the repository, the workload, the container image. The one place names are written down is the [naming registry](https://clearedstack.com/platform/registry.json), kept in [`clearedstack.com`](https://github.com/ClearedStack/clearedstack.com/blob/main/src/data/registry.json). `component.json` here is a copy of this component's entry. If the two disagree, the registry is right.

## Ground rules

- **No AI when it runs.** The same input gives the same output on any machine.
- **Stop instead of guessing.** A case with no rule is stopped and reported, never improvised.
- **Clean room.** Written from scratch. No code, text or design is copied from another project. The one exception is `testdata/contract`, which is a copy of the examples that define the contract with Muster, kept so the tests can replay them.
- **Public sources and made-up data only.** Nothing classified, controlled or taken from a real contract, in code, tests, issues or commit messages. Every host, notice, issue and submission in the fixtures is invented.

## License

[MIT](LICENSE). Trawl depends on nothing outside the Go standard library. ClearedStack is an independent personal project. It is not affiliated with, endorsed by, or sponsored by the U.S. Department of Defense or any other government agency.
