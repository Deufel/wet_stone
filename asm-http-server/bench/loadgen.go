// loadgen: a small HTTP load generator. Prints one summary line.
//   loadgen -url U -c 50 -d 5s            requests for a duration
//   loadgen -url U -c 8  -n 8             a fixed number of requests
//   loadgen -url U -put FILE -n 1         PUT a file
//   -keepalive                           reuse connections (default: off, one TCP connection per request)
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	url := flag.String("url", "", "target URL")
	c := flag.Int("c", 1, "concurrency")
	d := flag.Duration("d", 0, "duration (0 = use -n)")
	n := flag.Int("n", 0, "total requests (when -d is 0)")
	put := flag.String("put", "", "file to PUT instead of GET")
	ka := flag.Bool("keepalive", false, "reuse connections")
	label := flag.String("label", "", "label for the output line")
	flag.Parse()

	tr := &http.Transport{DisableKeepAlives: !*ka, MaxIdleConnsPerHost: *c, MaxConnsPerHost: 0}
	client := &http.Client{Transport: tr, Timeout: 120 * time.Second}

	var body []byte
	if *put != "" {
		var err error
		if body, err = os.ReadFile(*put); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	var reqs, errs, bytes int64
	var mu sync.Mutex
	var lats []time.Duration
	var remaining int64 = int64(*n)
	deadline := time.Now().Add(*d)
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < *c; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if *d > 0 {
					if time.Now().After(deadline) {
						return
					}
				} else if atomic.AddInt64(&remaining, -1) < 0 {
					return
				}
				t0 := time.Now()
				var resp *http.Response
				var err error
				if *put != "" {
					req, _ := http.NewRequest("PUT", *url, bytesReader(body))
					req.ContentLength = int64(len(body))
					resp, err = client.Do(req)
				} else {
					resp, err = client.Get(*url)
				}
				if err != nil {
					atomic.AddInt64(&errs, 1)
					continue
				}
				nb, _ := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode >= 400 {
					atomic.AddInt64(&errs, 1)
				}
				if *put != "" {
					nb = int64(len(body))
				}
				atomic.AddInt64(&bytes, nb)
				atomic.AddInt64(&reqs, 1)
				lat := time.Since(t0)
				mu.Lock()
				lats = append(lats, lat)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	el := time.Since(start).Seconds()
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	pct := func(p float64) time.Duration {
		if len(lats) == 0 {
			return 0
		}
		return lats[int(float64(len(lats)-1)*p)]
	}
	fmt.Printf("%-34s %8.0f req/s %9.1f MB/s  p50 %7.2fms  p99 %7.2fms  n=%d err=%d\n",
		*label, float64(reqs)/el, float64(bytes)/el/1e6,
		float64(pct(0.5).Microseconds())/1000, float64(pct(0.99).Microseconds())/1000, reqs, errs)
}

type br struct {
	b []byte
	i int
}

func (r *br) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
func bytesReader(b []byte) io.Reader { return &br{b: b} }
