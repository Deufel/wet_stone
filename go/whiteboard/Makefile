.PHONY: run build generate clean
build: generate
	go build -o whiteboard .
generate:
	templ generate
run: build
	./whiteboard
clean:
	rm -f whiteboard view/*_templ.go
