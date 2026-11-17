// Package depcheck holds the import-graph ratchet: a test that runs `go list
// -deps` over the session library packages and the model server binary and
// fails when a package links something the split forbids. It has no code of
// its own.
package depcheck
