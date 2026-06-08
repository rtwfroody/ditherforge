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

require (
	github.com/ctessum/go.clipper v0.1.2 // indirect
	github.com/hschendel/stl v1.0.4 // indirect
	github.com/james-bowman/sparse v0.0.0-20260216202247-495ee4f84d35 // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/mitchellh/colorstring v0.0.0-20190213212951-d06e56a500db // indirect
	github.com/qmuntal/draco-go v0.4.0 // indirect
	github.com/qmuntal/gltf v0.27.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/schollz/progressbar/v3 v3.19.0 // indirect
	golang.org/x/image v0.25.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
	golang.org/x/term v0.29.0 // indirect
	gonum.org/v1/gonum v0.17.0 // indirect
)

replace github.com/rtwfroody/ditherforge => ..
