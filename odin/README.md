# odin

Odin experiments.

- `tina/`   — [Tina](https://github.com/pmbanugo/tina), a thread-per-core,
              zero-allocation concurrency framework for Odin, as a git submodule
              (pinned at commit d129f67, 2026-10-12; Apache-2.0). Not modified.
              Clone with `--recurse-submodules`, or run `git submodule update --init`.
- `pulse/`  — a small demo built on Tina's HTTP extension and Datastar SDK. See its README.
- `whiteboard/` — a streamed SVG diagram board with snapping, on Tina + Datastar + mike.css.

Requires the Odin compiler (`omarchy pkg add odin` on Arch).
