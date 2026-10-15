SHELL := /bin/bash

AS = nasm
LD = ld
ASFLAGS = -f elf64 -Isrc/
LDFLAGS = -s

TARGET = server
SRC = src/server.asm
PAGES = src/page_head.html src/page_tail.html

# Directory to serve (override: make serve DIR=~/files)
DIR ?= ./files

# rsync demo settings
FROM        ?= /usr/share/licenses/
TO          ?= /tmp/asm-sse-demo/run-$(shell date +%s)/
RSYNC_FLAGS ?= -av --info=progress2 --no-inc-recursive --outbuf=L --bwlimit=2000

# fan-out demo settings
FANOUT_SRC ?= /usr/share/licenses
DEST1      ?= /tmp/asm-sse-demo/fanout-$(shell date +%s)/host1
DEST2      ?= /tmp/asm-sse-demo/fanout-$(shell date +%s)/host2
RATE       ?= 1500k

.PHONY: all clean serve run demo fanout bench

all: $(TARGET)

$(TARGET): $(SRC) $(PAGES)
	$(AS) $(ASFLAGS) $(SRC) -o $(TARGET).o
	$(LD) $(LDFLAGS) $(TARGET).o -o $(TARGET)

# File server: browse, download, upload and delete under DIR at http://127.0.0.1:8000
serve: $(TARGET)
	mkdir -p $(DIR)
	./$(TARGET) $(DIR)

run: serve

# Serve the rsync destination while rsync fills it; its progress streams to the page.
# "wait" pauses rsync until a browser connects.
demo: $(TARGET)
	mkdir -p $(TO)
	./$(TARGET) $(TO) <(rsync $(RSYNC_FLAGS) $(FROM) $(TO) 2>&1) wait

# Fan-out: archive FANOUT_SRC once and unpack it into DEST1 and DEST2 at the
# same time, with no intermediate file, while serving DEST1. tar's file list
# and pv's progress go to the browser; the archive bytes go to tee.
# The archive is measured once up front so pv has an exact total. "exec 3>&1"
# keeps a handle on the log pipe, because bash wires a pipeline's stdout before
# it evaluates the >(cat) redirections, which would otherwise write log text
# into the archive. "wait" pauses the pipeline until a browser connects.
# For real machines replace  >(tar xf - -C $(DEST1))  with  >(ssh host1 'tar xf - -C /backup').
fanout: $(TARGET)
	mkdir -p $(DEST1) $(DEST2)
	./$(TARGET) $(DEST1) <( exec 3>&1; \
	  tar cvf - -C $(dir $(FANOUT_SRC)) $(notdir $(FANOUT_SRC)) 2> >(cat >&3) \
	  | pv -f -i 0.2 -L $(RATE) -s "$$(tar cf - -C $(dir $(FANOUT_SRC)) $(notdir $(FANOUT_SRC)) | wc -c)" 2> >(cat >&3) \
	  | tee >(tar xf - -C $(DEST1)) >(tar xf - -C $(DEST2)) > /dev/null ) wait

# Compare against python3 -m http.server and Go's net/http FileServer (needs go; port 8000-8002 free)
bench: $(TARGET)
	bench/bench.sh

clean:
	rm -f $(TARGET) $(TARGET).o
