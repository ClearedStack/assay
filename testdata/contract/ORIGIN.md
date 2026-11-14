# Where these files come from

The other files in this folder are a copy, unchanged, of `contract/v1/examples/` in the Muster repository, taken from the branch `claude/repo-build-prompt-1csgx6` at commit `470dddf`: the copy made at `a362cb1`, plus the two examples added since (`refused-400-bad_field-logbook.json` and `refused-422-unknown_value-logbook.json`). Muster's `contract/v1/CONTRACT.md` is the contract; these are its saved examples.

Do not edit the files here. When Muster's contract changes, copy the folder again and run `go test .`. The test `TestContractExamples` in `contract_test.go` replays every file and fails on any file it does not know how to place.
