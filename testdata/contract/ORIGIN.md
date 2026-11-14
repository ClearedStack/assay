# Where these files come from

The other files in this folder are a copy, unchanged, of `contract/v1/examples/` in the Muster repository on its `main` branch (at `1c71bad` on Plainforge, `2c78026` on GitHub; the examples last changed at `470dddf`, and both copies are byte for byte the same). Muster's `contract/v1/CONTRACT.md` is the contract; these are its saved examples.

Do not edit the files here. When Muster's contract changes, copy the folder again and run `go test .`. The test `TestContractExamples` in `contract_test.go` replays every file and fails on any file it does not know how to place.
