ODIN ?= odin
# One SSE event carries the whole board, so raise Tina's per-connection egress buffer.
DEFINES = -define:HTTP_EGRESS_BUFFER_SIZE=57344
.PHONY: run build clean
build:
	$(ODIN) build . $(DEFINES) -out:whiteboard
run: build
	./whiteboard
clean:
	rm -f whiteboard
