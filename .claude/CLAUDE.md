## Project conventions

- For throwaway/debugging Python scripts, write them to `devscripts/` and execute from there. Do not use `python3 -c` with multiline scripts on the command line.
- Use `uv` venvs for all Python work.
- After making code changes, always verify `go test` is still passing before considering the work done.
