ODIN ?= odin
.PHONY: run build clean
build:
	$(ODIN) build . -out:pulse
run: build
	./pulse
clean:
	rm -f pulse
