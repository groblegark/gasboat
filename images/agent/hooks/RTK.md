# RTK (Rust Token Killer)

RTK is active. CLI commands are transparently proxied through `rtk` via a PreToolUse hook.

- Commands like `git status`, `cargo test`, `go test` are auto-rewritten to `rtk <cmd>`
- RTK filters noisy output (passing tests, progress bars, verbose logs) before it reaches context
- Commands already prefixed with `rtk` or containing heredocs pass through unchanged
- To check token savings: `rtk gain`
- To bypass RTK for a specific command: run the command directly without the hook
