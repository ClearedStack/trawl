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

> **Status: built, not yet deployed.** The code and its tests are here and pass. Trawl has only ever run against a stand-in for Muster, because Muster does not exist yet, and it has never run in a cluster. See [What is not done](#what-is-not-done).

Trawl is stage 01 of the ClearedStack pipeline. ClearedStack is an open pipeline that finds ideas for government-contracting tools, screens and scores them, and builds each one from the public rule. Trawl is meant to be understood and used without the rest.

## What works today

- **One address in, one canonical address out.** `trawl normalize` reads addresses on standard input and prints each in its canonical form, with its SHA-256 hash and whether it has been seen before. Sixteen rules do the work, and each has a file of saved examples.
- **Each address is recorded once.** A new hash gets the next `PA-XXXX` identifier. A hash already on record is logged as a sighting of the existing identifier.
- **A worker loop.** `trawl work` leases items from the hub, always `urgent` before `normal` before `backfill`, keeps the lease alive with heartbeats, and reports each item as complete, failed or needing a sub-tool.
- **Four sub-tools**, one per source: `sam-gov`, `github-issues`, `web-sweep` and `site-form`.
- **A spool.** Finished work is written to disk before the hub is told. If the hub is away, the work waits and is sent in order when the hub returns, even if Trawl itself restarted in between.
- **Three error codes and nothing else.** `E_TIMEOUT`, `E_PARSE` and `E_NEEDS_SUBTOOL`, each with a reason from a fixed list and a 12-character fingerprint. No error carries an address, a machine name or a credential.
- **No AI and no clock in the output.** The same items in the same order give the same bytes. A test runs the whole pipeline twice and compares.
- **Small.** The built binary drained 7,320 made-up records from the stand-in hub with a peak of about 11 MiB of memory, against a budget of 30 MiB. The binary reports its own peak when it stops.

## Run it

You need Go 1.24 or later.

```sh
go build -o trawl ./cmd/trawl

# Normalize, hash and remember addresses. The store is a plain text file.
printf 'HTTPS://Tools.Agency-Example.GOV/wage/index.html?utm_source=news#top\n' \
  | ./trawl normalize -store trawl.store
# url     https://tools.agency-example.gov/wage
# sha256  4f224a35258b04c87bebd7a0ab0edd93a83c8ccb7b9434e23fb9970f355f640d
# seen    no
# id      PA-0001

# Run the worker against a hub.
./trawl work -muster-url http://HUB-ADDRESS -spool /var/spool/cs
```

`trawl work` takes these flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `-muster-url` | none, required | Address of the hub. |
| `-token-file` | the Kubernetes ServiceAccount token path | File holding the token sent to the hub. Empty sends none. |
| `-spool` | `/var/spool/cs` | The one directory Trawl writes to. |
| `-drain` | off | Stop once every lane and the spool are empty. |

### Tests

```sh
go test ./...          # everything, including the saved examples
go test -short ./...   # skips the test that builds and runs the binary
```

The saved examples are the `testdata` folders. To change behaviour on purpose, change the code, run `TRAWL_UPDATE_GOLDEN=1 go test ./...` to rewrite the expected files, and review the difference like any other change. Without that variable, any change in output fails a test.

### Container image

The image is built from source with [ko](https://ko.build); there is no Dockerfile. Settings are in [`.ko.yaml`](.ko.yaml).

```sh
KO_DOCKER_REPO=lazarette.clearedstack.svc.cluster.local:5000 \
  ko build --base-import-paths ./cmd/trawl
```

This was checked with ko v0.19.1 by building to a file without pushing. The image came to about 3.7 MB, runs as user 65532 and starts `/ko-app/trawl`, so a pod passes `work -muster-url ...` as arguments. It has not been pushed to a registry or run in a cluster.

## In this repository

| Path | What it is |
| --- | --- |
| [`SPEC.md`](SPEC.md) | The specification: what Trawl does, its interfaces, how it fails and how it is kept safe. |
| [`PROMPT.md`](PROMPT.md) | The prompt this build was started with. |
| [`component.json`](component.json) | This component's entry in the ClearedStack naming registry. |
| [`cmd/trawl`](cmd/trawl) | The `trawl` command: `normalize`, `work`, `version`. |
| [`tools/`](tools) | The sub-tools. [`registry.yaml`](tools/registry.yaml) lists them; each folder holds `match.yaml`, the Go logic and `testdata/`. |
| [`internal/normalize`](internal/normalize) | The address rules and their saved examples. |
| [`internal/dedupe`](internal/dedupe) | SHA-256 hashing, `PA-XXXX` identifiers, and the record-or-sighting decision. |
| [`internal/muster`](internal/muster) | The interface to the hub, an in-memory stand-in, and an HTTP client. |
| [`internal/spool`](internal/spool) | The on-disk queue of finished work. |
| [`internal/worker`](internal/worker) | The lease, process, stage and acknowledge loop. |
| [`internal/fetch`](internal/fetch), [`internal/robots`](internal/robots) | Reading public addresses, and obeying `robots.txt`. |
| [`internal/intake`](internal/intake), [`internal/screen`](internal/screen) | Rules every candidate passes: marking screen, markup stripping, normalization. |
| [`internal/errs`](internal/errs) | The three public error codes. |
| [`internal/store`](internal/store) | The plain file store behind `trawl normalize`. The worker never uses it. |
| [`brand/`](brand) | The Trawl logo and mark, for light and dark backgrounds. |
| [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) | The license of the one outside library. |

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
| `syntax` | Refuses an address that is empty, longer than 2,048 bytes, has control characters, a backslash, no scheme, no host, a bad port or a bad percent escape. |
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

A matching rule compares an item's `source` and `format` with the rule in `match.yaml`. Exactly one rule must match. If none does, or more than one, the item is paused as `needs_subtool`.

| Sub-tool | Matches | Input | What it records | Saved cases |
| --- | --- | --- | --- | --- |
| [`sam-gov`](tools/sam-gov) | `sam-gov` / `sam-gov.opportunities.v2` | inline or fetched | From each notice: the additional-information link, each attachment, and each address in the description text. An address containing "calculator" is marked as one. | 16 |
| [`github-issues`](tools/github-issues) | `github-issues` / `github-issues.v1` | inline or fetched | From each issue labelled `tool-suggestion` or `paperwork-suggestion` and written with the matching issue form: the address and the name. | 19 |
| [`web-sweep`](tools/web-sweep) | `web-sweep` / `web-sweep.page.v1` | fetched only | From one HTML page: each link whose text or path contains a word from a fixed list, such as "calculator", "template" or "checklist". | 10 |
| [`site-form`](tools/site-form) | `site-form` / `site-form.v1` | inline only | A candidate address, or a proposed edit to one field of a record. | 27 |

Adding a sub-tool means adding a folder with those three parts, a line in `tools/registry.yaml` and a line in `tools/tools.go`. A test fails if a folder is not registered or a registered folder lacks a part.

## Errors

| Code | Meaning | What the hub does with it |
| --- | --- | --- |
| `E_TIMEOUT` | Something outside took too long, was unreachable or answered with a server error. | Puts the item back in its lane to be tried again. |
| `E_PARSE` | The data was broken or failed validation. | Halts that item. |
| `E_NEEDS_SUBTOOL` | The data was valid and no rule covers it. | Pauses the item until a rule ships, then replays it. |

An error is printed as, for example, `E_PARSE tool=site-form reason=honeypot fp=f9751ff461cb`. The fingerprint of an `E_NEEDS_SUBTOOL` depends only on the kind of case, so every item paused for the same reason reports the same one.

When one entry inside an item is broken, such as one notice in a page, that entry is refused and the rest of the item goes on. When one entry is valid and unfamiliar, the whole item is paused, because the contract can pause an item and not part of one. Replaying it later is safe: nothing is recorded twice.

## Requirements in `SPEC.md` and where each stands

### Interfaces and data contracts

| Requirement | State | Where it is tested |
| --- | --- | --- |
| `lease`, `heartbeat`, `complete`, `fail`, `needs_subtool` over HTTP with the ServiceAccount token, contract `v1` | Built against the stand-in | `internal/muster`, `internal/worker` |
| Lanes drained `urgent`, then `normal`, then `backfill` | Built | `TestLanesAreDrainedInOrder` |
| Lowercase scheme and host | Built | `internal/normalize` |
| Strip tracking and session parameters and fragments | Built | `internal/normalize` |
| Remove trailing slashes and default index filenames | Built | `internal/normalize` |
| Public-only scheme and host | Built | `internal/normalize`, `internal/fetch` |
| SHA-256 of the canonical address, checked through the hub | Built | `internal/dedupe`, `internal/worker` |
| Existing hash becomes a sighting, no duplicate | Built | `TestEachAddressIsRecordedOnce` |
| New hash mints a `PA-XXXX` identifier and is queued for the next stage | Built | `internal/worker`, `tools/testdata/pipeline.golden.json` |
| Finished work staged in `/var/spool/cs` before the hub is told, flushed in order | Built | `TestHubRestartMidBatch`, `TestSpoolIsFlushedInOrder` |

### Failure modes

| Failure | State | Where it is tested |
| --- | --- | --- |
| Unknown source format pauses the item and carries on | Built | `TestFailuresDoNotStopTheRest` |
| Paused items are replayed once a rule exists | Built in the stand-in only | `TestFailAndNeedsSubtool` |
| Upstream timeout leaves the item in the queue | Built | `TestFetchOutcomes`, `internal/fetch` |
| Malformed payload halts that item with a sanitized fingerprint | Built | every sub-tool's `testdata` |
| Hub unavailable: work is kept and flushed later | Built | `TestHubRestartMidBatch` |

### Security

| Rule | State | Where it is tested |
| --- | --- | --- |
| No database access; all state goes through the hub | Built. Trawl holds no database client or credential. | By construction |
| Form shield 1: proof of work | Verified by Trawl: 18 leading zero bits, bound to the submission | `tools/site-form` |
| Form shield 2: honeypot | Verified by Trawl | `tools/site-form` |
| Form shield 3: rate limit on a daily-salted hash, never a raw IP | Counted through the hub: 5 a day per hash. A submission with an `ip` key is refused. | `TestRateLimit`, `raw-ip-refused` |
| Form shield 4: 4 KB schema with markup stripped | Verified by Trawl | `tools/site-form`, `internal/screen` |
| Form shield 5: owner approval before an edit takes effect | Not Trawl's. An edit leaves Trawl as a proposal and changes nothing. | n/a |
| Never ignores `robots.txt` | Built. Asked before every address and every redirect step. | `internal/fetch`, `internal/robots` |
| Never creates accounts, requests demos, or passes paywalls or CAPTCHAs | Built. Trawl sends `GET` only, with no cookies and no credentials, and stops at a sign-in. | `internal/fetch` |
| Never accepts controlled information | Partly. Text carrying a control marking is refused and not stored. | `internal/screen` |
| Runs unprivileged with a read-only filesystem | The binary writes only under the spool directory. The pod settings are not in this repository. | See below |

## What is not done

- **Muster.** It does not exist, so Trawl has only run against `internal/muster`'s in-memory stand-in. The HTTP wire format is provisional.
- **Kubernetes manifests.** There is no Deployment, ServiceAccount, NetworkPolicy or volume for the spool here. Non-root, dropped capabilities and a read-only root filesystem are settings of the pod, and they need names `component.json` does not carry: the hub's address and the priority class.
- **Live SAM.gov.** Its search API needs a key and Trawl holds no credentials. A `sam-gov` item must carry its page inline, or name an address that needs no key.
- **Host names outside ASCII.** They are paused as `E_NEEDS_SUBTOOL` (`idn_host`). Converting them correctly needs tables the Go standard library does not have.
- **Unmarked sensitive text.** Trawl refuses text with a control marking. It cannot recognise controlled information or bid figures that carry no marking. It limits the exposure by keeping only an address, a title of at most 120 characters, and the value of a proposed edit.
- **Mirror links.** `SPEC.md` mentions "the sighting or mirror link". Trawl records a sighting with where the address was seen. It has no separate idea of a mirror.
- **Path clean-up beyond the specification.** `.` and `..` segments in an absolute address, doubled slashes inside a path, and percent escapes of ordinary letters are left as they are.
- **Opening a task for a paused item** and **shipping a new sub-tool** belong to other components. Trawl's part is the stable fingerprint.
- **Pages that are not HTML or not UTF-8.** A fetched PDF is paused as `not_html`.
- **A proxy.** The fetcher connects directly and ignores proxy settings.

## Where the specification was silent

`SPEC.md` leaves these open. The build chose as follows, and each choice is a proposed addition to `SPEC.md`.

| Question | Choice | Reason |
| --- | --- | --- |
| How does Trawl read state? The contract names no call. | Two added calls: `lookup` (which hashes are recorded, and the newest sequence number) and `rate` (add a member to a named set, return its size). | Deduplication "via Muster" needs a read. The rate limit needs a counter and Trawl has no database. |
| Who mints `PA-XXXX`? | Trawl, as decided for this build. `complete` carries the new records and the sequence number Trawl expected; the hub must refuse the whole completion if the number moved. Trawl then reads again and re-decides, up to five times. | Lets several Trawl replicas run without two addresses getting one identifier. |
| Identifier format | `PA-` and a decimal number padded to four digits. After `PA-9999` comes `PA-10000`. | Keeps existing identifiers valid for ever. |
| Wire format | `POST` of JSON to `/v1/lease`, `/v1/heartbeat`, `/v1/lookup`, `/v1/rate`, `/v1/complete`, `/v1/fail`, `/v1/needs_subtool`, with a `Contract-Version: v1` header and the token as a bearer token. | Something concrete to test against. |
| What is in an item? | `source`, `format`, `observed_at`, and exactly one of `fetch` (an address) or `payload` (the document). | The matching rules need something to match, and time must come from the hub for outputs to be repeatable. |
| Who schedules sweeps? | The hub, or whatever fills its queue. Trawl reads the one page an item names and follows nothing. | A worker that only leases cannot schedule. |
| What makes a link a candidate? | A whole word from a fixed list in its text or path. The list is `Words` in `tools/web-sweep/websweep.go`. | The specification gives no rule. This one is provisional and easy to change. |
| What does a suggestion issue look like? | Two labels and two issue forms, described at the top of `tools/github-issues/githubissues.go`. | The specification says "structured" without giving the structure. |
| What is `ownership_map.json`? | A stand-in at `tools/site-form/ownership_map.json`: the fields an edit may target, each with a longest length. | The real file is not in this repository. |
| What is a "sanitized fingerprint"? | The first 12 hex characters of a SHA-256 digest of the offending bytes, or of the kind of case for `E_NEEDS_SUBTOOL`. | Lets identical failures be grouped without showing the data. |
| A lease that ended while its result waited in the spool | The result is dropped and the item is processed again when the hub hands it out. | The hub is the only authority on who holds an item. |

## Names

`trawl` is this component's identifier everywhere: the repository, the workload, the container image. The one place names are written down is the [naming registry](https://clearedstack.com/platform/registry.json), kept in [`clearedstack.com`](https://github.com/ClearedStack/clearedstack.com/blob/main/src/data/registry.json). `component.json` here is a copy of this component's entry. If the two disagree, the registry is right.

## Ground rules

- **No AI when it runs.** The same input gives the same output on any machine.
- **Stop instead of guessing.** A case with no rule is paused and reported, never improvised.
- **Clean room.** Written from scratch. No code, text or design is copied from another project.
- **Public sources and made-up data only.** Nothing classified, controlled or taken from a real contract, in code, tests, issues or commit messages. Every host, notice, issue and submission in the fixtures is invented.

## License

[MIT](LICENSE). The one outside library and its license are in [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md). ClearedStack is an independent personal project. It is not affiliated with, endorsed by, or sponsored by the U.S. Department of Defense or any other government agency.
