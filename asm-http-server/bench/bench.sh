#!/bin/bash
# Benchmark the asm server against python3 -m http.server and Go's net/http FileServer.
# Usage: bench/bench.sh            (needs: go, python3, port 8000-8002 free)
set -u
cd "$(dirname "$0")/.."
B=$(mktemp -d /tmp/asm-bench.XXXX); trap 'kill $(jobs -p) 2>/dev/null; rm -rf $B' EXIT
mkdir -p $B/root $B/bin
head -c 1024      /dev/urandom > $B/root/1k.bin
head -c 1048576   /dev/urandom > $B/root/1m.bin
head -c 104857600 /dev/urandom > $B/root/100m.bin
for i in $(seq 1 200); do : > $B/root/file-$i.txt; done
cd bench && go build -o $B/bin/loadgen loadgen.go && go build -o $B/bin/gofs gofs.go && cd ..
make -s

./server $B/root > /dev/null 2>&1 &
python3 -m http.server 8001 --bind 127.0.0.1 -d $B/root > /dev/null 2>&1 &
$B/bin/gofs 8002 $B/root &
sleep 0.5
L=$B/bin/loadgen
run() {  # label port path [extra loadgen args]
  $L -label "$1" -url "http://127.0.0.1:$2$3" "${@:4}"
}
echo "machine: $(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2 | sed 's/^ //'), $(nproc) cores. Connection per request unless noted."
echo
echo "== small file (1 KB), 50 concurrent, 5 s =="
run "asm"           8000 /1k.bin -c 50 -d 5s
run "python http.server" 8001 /1k.bin -c 50 -d 5s
run "go FileServer" 8002 /1k.bin -c 50 -d 5s
run "go FileServer (keep-alive)" 8002 /1k.bin -c 50 -d 5s -keepalive
echo
echo "== directory listing (203 entries), 50 concurrent, 5 s =="
run "asm"           8000 / -c 50 -d 5s
run "python http.server" 8001 / -c 50 -d 5s
run "go FileServer" 8002 / -c 50 -d 5s
echo
echo "== 1 MB file, 20 concurrent, 5 s =="
run "asm"           8000 /1m.bin -c 20 -d 5s
run "python http.server" 8001 /1m.bin -c 20 -d 5s
run "go FileServer" 8002 /1m.bin -c 20 -d 5s
echo
echo "== 100 MB file, single stream, 3 requests =="
run "asm"           8000 /100m.bin -c 1 -n 3
run "python http.server" 8001 /100m.bin -c 1 -n 3
run "go FileServer" 8002 /100m.bin -c 1 -n 3
echo
echo "== 100 MB file, 6 concurrent streams, 6 requests =="
run "asm"           8000 /100m.bin -c 6 -n 6
run "python http.server" 8001 /100m.bin -c 6 -n 6
run "go FileServer" 8002 /100m.bin -c 6 -n 6
echo
echo "== upload 100 MB (PUT), asm only =="
run "asm PUT"       8000 /upload.bin -c 1 -n 3 -put $B/root/100m.bin
cmp -s $B/root/100m.bin $B/root/upload.bin && echo "   (uploaded file identical to source)" || echo "   (UPLOAD MISMATCH)"
echo
echo "== small file, 50 concurrent, while 4 slow clients drain 100 MB each =="
for i in 1 2 3 4; do curl -s --limit-rate 2M -o /dev/null http://127.0.0.1:8000/100m.bin & done
sleep 0.5
run "asm (under slow-client load)" 8000 /1k.bin -c 50 -d 5s
