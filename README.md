# wet_stone

Small servers, sharpened: experiments in fast, server-driven web apps.

| path | what |
|---|---|
| [`go/whiteboard`](go/whiteboard) | a live, multi-user diagram board: Go + templ + Datastar + embedded NATS + SQLite. The server draws the whole SVG for every viewer and streams it as a morph; commands go up, frames come down. History with branches. Live at [draw.deufel.dev](https://draw.deufel.dev). |
| [`odin/whiteboard`](odin/whiteboard) | the same board on Odin + [Tina](https://github.com/pmbanugo/tina) |
| [`odin/pulse`](odin/pulse) | a small demo on Tina's HTTP extension and Datastar SDK |
| [`odin/tina`](odin/tina) | Tina, as a git submodule (pinned) |
| [`asm-http-server`](asm-http-server) | an HTTP file server in x86-64 assembly |

Styling throughout is [mike.css](https://github.com/Deufel/css).

```bash
git clone --recurse-submodules https://github.com/Deufel/wet_stone
cd wet_stone/go/whiteboard && make run      # http://127.0.0.1:8081
```

## Credits

By Michael Deufel, written with Claude (Anthropic) in Claude Code.
MIT licensed (see [LICENSE](LICENSE)); `odin/tina` keeps its own Apache-2.0
license.
