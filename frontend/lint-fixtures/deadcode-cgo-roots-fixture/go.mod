// A module of its own, and that is load-bearing: the ratchet, deadcode and
// golangci-lint all work in packages (`go list ./...`), and a nested module is
// not one of them. So these files are read by ../check-deadcode.test.mjs and
// never compiled by anything in this repository.
module cgorootsfixture

go 1.26
