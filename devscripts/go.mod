// devscripts is a nested module so the parent's `go mod tidy` and
// `wails dev` skip this directory entirely. Without this, any
// devscript that imports an internal/ package the current branch
// lacks (e.g. internal/cellslicer on main) trips dependency
// resolution for the whole project. devscripts/ is gitignored, so
// each developer keeps their own scripts here without affecting
// anyone else; this go.mod is the one tracked file that makes the
// isolation work.
//
// Failure stays local: if a script imports a parent package that
// the checked-out branch doesn't have, only `cd devscripts && go
// run ./<name>/` errors out — `go mod tidy`, `go test ./...`, and
// `wails dev` at the parent stay green.
//
// Two run modes both work:
//   - Directory script: `cd devscripts && go run ./<name>/` resolves
//     imports through this module's replace directive.
//   - Single-file script: `go run devscripts/<name>.go` from the
//     parent still works — single-file `go run` mode treats the
//     file as outside any module and resolves imports via the
//     parent's go.mod, so loose top-level scripts (some with
//     `//go:build ignore`) keep their existing workflow.
module github.com/rtwfroody/ditherforge/devscripts

go 1.24.0

require github.com/rtwfroody/ditherforge v0.0.0

replace github.com/rtwfroody/ditherforge => ..
