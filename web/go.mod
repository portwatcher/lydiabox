// This file only marks web/ as a separate Go module, so that `go build ./...`,
// `go vet ./...`, and `go test ./...` in the repository root do not descend
// into web/node_modules (some npm packages ship Go sources). web/ has no Go
// code.
module github.com/portwatcher/lydiabox/web

go 1.24
