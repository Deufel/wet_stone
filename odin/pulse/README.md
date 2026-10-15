# pulse — a Tina + Datastar demo

One Counter isolate. Every browser that opens the page subscribes to it over
Server-Sent Events and sees the same count, uptime and generation number. Two
buttons send it messages: **+1** and **crash the counter**.

```bash
make run          # builds and starts; open http://127.0.0.1:8080 in two windows
```

## What to watch

1. Click **+1** in one window. Both windows update at once: the Counter
   broadcast the new value to every subscriber.
2. Click **crash the counter**. The Counter isolate panics on purpose. Its
   supervisor rebuilds it from the boot spec: new handle, empty state. Within
   two seconds every page logs the restart, the count is 0 again and the
   generation number has gone up. The HTTP listener on the same shard never
   stopped; the page never reconnected.
3. Keep clicking crash. The Counter sits in its own supervision group with a
   large restart budget, so it always comes back. Remove that group in
   `main.odin` and the root group's default budget will eventually quarantine
   the whole shard, which takes the HTTP listener with it. That is Tina's
   "let it crash, but contain it" policy at work, both ways.

## How it fits together

```
browser ──GET /events──▶ connection isolate ──TAG_SUBSCRIBE──▶ Counter isolate
        ◀──SSE patches── connection isolate ◀──TAG_FEED───────  (timer tick / bump)
        ──POST /bump────▶ connection isolate ──TAG_BUMP────────▶ Counter isolate
```

- `Counter` is a plain struct with a handler function. It never touches a
  socket. It reacts to messages (`TAG_BUMP`, `TAG_TICK`, `TAG_SUBSCRIBE`, ...)
  and sends `Feed_Msg` values to subscriber handles. A stale handle (a browser
  that left) is dropped on the next send.
- `/events` is an event-style route. On `Send_Ready` it subscribes, then parks
  with `expect_notification`. Each `TAG_FEED` becomes a `datastar-patch-signals`
  event, so the page needs no JavaScript of its own: `data-text="$count"`.
- If no feed arrives within the parking window, the Counter has probably been
  restarted (new handle, empty subscriber list). The route re-subscribes to
  the current handle. That is how a crash stays invisible to the browser.
- `/bump` and `/crash` send one message and answer with an empty Datastar
  stream, which is what `@post()` expects.
- Everything is sized at boot: 256 subscribers, a 256-slot mailbox, one
  Counter slot. No allocation happens after `tina_start`.

## Files

- `main.odin`   the whole demo, about 400 lines, imports `../tina/src`
- `Makefile`    `make run`, `make build`, `make clean`
