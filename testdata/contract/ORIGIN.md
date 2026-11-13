# Where these files come from

Everything else in this folder is a copy, unchanged, of `contract/v1/examples/` in the Muster repository, taken from the branch `claude/repo-build-prompt-1csgx6` at commit `7706321`. Muster's `contract/v1/CONTRACT.md` is the contract; these are its saved examples.

Do not edit them here. When Muster's contract changes, copy the folder again and run `go test .`. The test `TestContractExamples` in `contract_test.go` replays every file and fails on any file it does not know how to place.
