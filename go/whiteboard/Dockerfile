# The Go whiteboard: one static binary (modernc SQLite is pure Go, so no cgo),
# its assets embedded. The templ output (view/*_templ.go) is committed, so the
# build needs no templ. The SQLite history lives on the /data volume.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /whiteboard .

FROM alpine:3.22
RUN adduser -D -H -u 10001 board && mkdir /data && chown board /data
COPY --from=build /whiteboard /usr/local/bin/whiteboard
USER board
VOLUME /data
EXPOSE 8080
# /healthz answers 200 while the server and its database are up (busybox wget)
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/whiteboard", "-addr", "0.0.0.0:8080", "-db", "/data/whiteboard.db"]
