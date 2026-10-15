# A personal file server in x86-64 assembly

A small HTTP server written entirely in x86-64 assembly for Linux. No libc,
one process, one thread, `epoll`. It serves a directory with a browsable page,
downloads, uploads and deletes, and pushes a live activity feed to every open
page using [Datastar](https://data-star.dev)'s Server-Sent Events protocol.

```bash
make serve DIR=~/files     # http://127.0.0.1:8000
```

| Request            | Result                                                   |
|--------------------|----------------------------------------------------------|
| `GET /dir/`        | listing page (Datastar), upload box, delete buttons      |
| `GET /dir`         | 301 to `/dir/`                                           |
| `GET /file`        | download via `sendfile`, Content-Type from the extension |
| `PUT /file`        | upload (`curl -T photo.jpg https://host/photo.jpg`)      |
| `DELETE /file`     | unlink                                                   |
| `GET /_events`     | SSE feed: one line per request, plus any piped source    |

## Putting it on the web: files.deufel.dev

The assembly server has no TLS and no authentication by design. Caddy sits in
front of it: it fetches a Let's Encrypt certificate automatically, asks for a
username and password, and proxies to `localhost:8000`.

```
browser ──HTTPS + basic auth──▶ Caddy :443 ──HTTP──▶ ./server :8000 (localhost only)
```

1. **DNS.** In Cloudflare, add an `A` record `files` → your public IP, with the
   proxy **off** (grey cloud, "DNS only"). Caddy must see the client directly to
   get its certificate, and Cloudflare's proxy caps uploads at 100 MB on the
   free plan.
2. **Ports.** 80 and 443 must reach this machine. On a VPS that is a firewall
   rule; at home it is a port forward on the router. If your home IP changes,
   update the record (or use a dynamic-DNS updater).
3. **Install.** `sudo ./deploy/install.sh` installs Caddy, builds the server,
   asks you for the password, and enables both as systemd services. Files live
   in `~/files`.

From then on: https://files.deufel.dev in a browser, or from a terminal:

```bash
curl -u mike -T backup.tar.zst https://files.deufel.dev/backup.tar.zst
curl -u mike -O https://files.deufel.dev/backup.tar.zst
curl -u mike -X DELETE https://files.deufel.dev/backup.tar.zst
```

## The live feed and pipelines

The feed shows every request as it happens. The server can also take a pipe
as a second argument and stream its lines into the same feed; a line containing
`NN%` drives the progress bar. That makes it a live window on any shell
pipeline, using bash process substitution:

```bash
make demo      # serves the rsync destination while rsync fills it, with progress
make fanout    # tar | pv | tee into two destinations at once, no temp file
./server ~/files <( some-long-job 2>&1 )
```

The fan-out form is the real-world one: replace `>(tar xf - -C dir)` with
`>(ssh host 'tar xf - -C /backup')` to push one archive to two machines while
watching from the page. Note the `exec 3>&1; ... 2> >(cat >&3)` pattern in the
Makefile: bash wires a pipeline's stdout before it evaluates redirections, so
without the saved descriptor the log text would end up inside the archive.

## How it works

- `epoll` multiplexes the listening socket, the optional source pipe and every
  browser connection. Each registered descriptor carries a tag so the loop knows
  what fired.
- Downloads and uploads `fork`: the child does `sendfile` or streams the body
  into the file and exits; the parent keeps serving. `SIGCHLD` is ignored so
  children are reaped automatically.
- Paths are percent-decoded, NUL bytes and any `..` segment are rejected, and the
  process `chdir`s into the served directory and only opens relative paths.
- Listing pages are built from `src/page_head.html` + `getdents64` output +
  `src/page_tail.html`, embedded in the binary with `incbin`.
- The feed speaks Datastar's protocol: `datastar-patch-elements` to append log
  lines, `datastar-patch-signals` for progress.

## Is it fast?

`make bench` builds a small Go load generator and runs the same scenarios
against this server, `python3 -m http.server`, and Go's standard-library
`http.FileServer`. On a 6-core laptop over loopback, one TCP connection per
request:

| scenario                              | asm        | Go FileServer | python http.server |
|---------------------------------------|------------|---------------|--------------------|
| 1 KB file, 50 concurrent              | 24.6k req/s | 29.3k req/s  | 0.8k req/s         |
| directory listing, 203 entries        | 29.1k req/s | 10.8k req/s  | 0.26k req/s        |
| 1 MB file, 20 concurrent              | 9.7 GB/s   | 11.0 GB/s     | 1.6 GB/s           |
| 100 MB file, 6 concurrent streams     | 12.5 GB/s  | 12.2 GB/s     | 7.6 GB/s           |
| 1 KB file while 4 slow clients drain 100 MB each | 25.1k req/s | | |

Large transfers are bound by the kernel's `sendfile`, so every server lands in
the same place. Small requests are bound by TCP connection setup and the
per-request `fork`; Go with keep-alive connections reaches 75k req/s on the
same test, which is the main thing this server leaves on the table by closing
every connection. None of this matters behind a home or VPS uplink, where the
network is thousands of times slower than any row above.

## Limits

- Only localhost, port 8000, hardcoded. Caddy provides everything else.
- No Range requests (no video seeking), no directory creation, no rename.
- Request headers must fit in 8 KB and arrive in one read; chunked uploads are
  rejected with 411 (browsers and curl send Content-Length).
- Up to 64 simultaneous feed viewers; listing pages truncate past 256 KB.
- Symlinks inside the served directory are followed.

## Requirements

- Linux x86-64, NASM, ld (binutils)
- bash, rsync, tar and pv for the demos; Caddy for the web deployment

## Layout

- `src/server.asm`                 the server
- `src/page_head.html`, `src/page_tail.html`  the listing page, embedded at build time
- `deploy/Caddyfile`, `deploy/asm-files.service`, `deploy/install.sh`
- `Makefile`                       `serve`, `demo`, `fanout`
