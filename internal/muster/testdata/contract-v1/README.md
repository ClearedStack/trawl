# Muster contract v1 examples

The files in `examples/` are copied unchanged from the Muster repository:
folder `contract/v1/examples/`, commit `a362cb1` on branch
`claude/repo-build-prompt-1csgx6`. The contract they belong to is
`contract/v1/CONTRACT.md` in that repository.

Two tests in this package replay them:

- `TestContractExamplesThroughClient` replays every example of a call
  Trawl's client can make. It checks that the client sends exactly the
  saved request body, and reads the saved answer the way the worker
  relies on.
- `TestContractExamplesAgainstFake` replays every example the fake hub
  covers against the fake, and compares the status and body exactly. That
  keeps the fake, which the rest of Trawl's tests run against, in step
  with the contract.

To update: copy the folder again, change the commit above, and run the
tests. An example the fake does not cover must be listed, with the
reason, in `notCovered` in `contract_test.go`, or the test fails.
