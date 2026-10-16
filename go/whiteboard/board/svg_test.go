package board

import (
	"strings"
	"testing"
)

func TestCleanSVG(t *testing.T) {
	in := `<svg viewBox="0 0 10 10" onload="alert(1)"><script>alert(2)</script>` +
		`<circle cx="5" cy="5" r="4" fill="url(#g)" onclick="x()"/><a href="http://evil"><rect width="1" height="1"/></a>` +
		`<foreignObject><iframe src="x"/><div>hi</div></foreignObject><use href="https://evil/x.svg#a"/><use href="#ok"/>` +
		`<rect style="background:url(http://evil)" width="2" height="2"><animate attributeName="x" from="0" to="5" dur="2s" repeatCount="indefinite"/></rect>` +
		`<text x="1" y="9">E = mc²</text></svg>`
	out, err := CleanSVG(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"script", "alert", "onload", "onclick", "evil", "iframe", "<a ", "<a>"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%q kept: %s", bad, out)
		}
	}
	for _, good := range []string{`fill="url(#g)"`, `<animate`, `href="#ok"`, `E = mc²`, `viewBox="0 0 10 10"`} {
		if !strings.Contains(out, good) {
			t.Fatalf("%q lost: %s", good, out)
		}
	}
	if _, err := CleanSVG("<div>no</div>"); err == nil {
		t.Fatal("not an svg, kept")
	}
}

func TestCleanSVGTriggers(t *testing.T) {
	src := `<svg viewBox="0 0 100 100"><circle r="10" style="--fg: 0.8; --hue-shift: 120" fill="currentColor"><animate data-anim="go" begin="indefinite" attributeName="cx" from="10" to="90" dur="1s"/></circle>` +
		`<g data-trigger="go" data-on:click="alert(1)"><rect width="20" height="10"/></g>` +
		`<foreignObject x="0" y="80" width="40" height="20"><button data-trigger="go" class="suc" onclick="x()" style="position:fixed">Play <b>it</b></button><script>x()</script><iframe/></foreignObject>` +
		`<g data-trigger="a')+alert(1)+('"/></svg>`
	out, err := CleanSVG(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"alert", "onclick", "script", "iframe", "position"} {
		if strings.Contains(out, bad) {
			t.Errorf("kept %q: %s", bad, out)
		}
	}
	for _, want := range []string{`data-anim="go"`, `<button xmlns="http://www.w3.org/1999/xhtml" data-trigger="go"`, `<b>it</b>`, `data-on:click__stop="el.closest(`, `--hue-shift: 120`} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q: %s", want, out)
		}
	}
	again, err := CleanSVG(out)
	if err != nil || again != out {
		t.Errorf("not idempotent:\n%s\n%s", out, again)
	}
}
