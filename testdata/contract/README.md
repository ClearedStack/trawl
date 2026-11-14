# Muster contract v1 examples

The JSON files in this folder are copied unchanged from the Muster repository:
folder `contract/v1/examples/`, commit `470dddf` on branch
`claude/repo-build-prompt-1csgx6`. The contract they belong to is
`contract/v1/CONTRACT.md` in that repository.

Two tests in `internal/muster` replay them:

- `TestContractExamplesThroughClient` replays all 52 saved examples as
  subtests:
  - For every worker/item call Trawl's client can send, it checks that the
    client sends the exact saved request bytes and decodes the saved HTTP
    response the way the worker relies on.
  - For refusal examples that a well-formed client cannot provoke on its
    own (such as malformed JSON, unknown fields or wrong HTTP methods), it
    replays Muster's saved HTTP status and refusal body through `*muster.HTTP`
    and checks status/code/cause decoding, retry/refusal classification,
    and that Muster's `reason` sentence is never repeated in `Error()`.
  - For endpoints belonging to other callers (`release`, `edits`,
    `items-site-form`, `health`, `feed`, `healthz`), it checks the caller,
    method and path against `CONTRACT.md`.
- `TestContractExamplesAgainstFake` replays every example the fake hub
  covers against the fake, and compares the status and body exactly. That
  keeps the fake, which the rest of Trawl's tests run against, in step
  with the contract.

To update: copy the folder again, change the commit above, and run the
tests. An example the fake does not cover must be listed, with the
reason, in `notCovered` in `internal/muster/contract_test.go`, or the test fails.
