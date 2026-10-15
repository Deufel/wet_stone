// gofs: Go's standard-library static file server, as a fast reference point.
package main

import (
	"net/http"
	"os"
)

func main() {
	http.ListenAndServe("127.0.0.1:"+os.Args[1], http.FileServer(http.Dir(os.Args[2])))
}
