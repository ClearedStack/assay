# Where these files come from

The other files in this folder are a copy, unchanged, of `contract/v1/examples/` in the Muster repository, taken from the branch `claude/repo-build-prompt-1csgx6` at commit `a362cb1`. Muster's `contract/v1/CONTRACT.md` is the contract; these are its saved examples.

One file is the exception: `complete-release.json` was in an earlier copy and is no longer among Muster's examples. It is left here only until someone removes it, and it describes nothing in the current contract.

Do not edit the files here. When Muster's contract changes, copy the folder again and run `go test .`. The test `TestContractExamples` in `contract_test.go` replays every file and fails on any file it does not know how to place.
