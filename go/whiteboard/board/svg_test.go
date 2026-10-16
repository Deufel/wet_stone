package board

import (
	"strings"
	"testing"
)

func TestCleanSVG(t *testing.T) {
	in := `<svg viewBox="0 0 10 10" onload="alert(1)"><script>alert(2)</script>` +
		`<circle cx="5" cy="5" r="4" fill="url(#g)" onclick="x()"/><a href="http://evil"><rect width="1" height="1"/></a>` +
		`<foreignObject><div>hi</div></foreignObject><use href="https://evil/x.svg#a"/><use href="#ok"/>` +
		`<rect style="background:url(http://evil)" width="2" height="2"><animate attributeName="x" from="0" to="5" dur="2s" repeatCount="indefinite"/></rect>` +
		`<text x="1" y="9">E = mc²</text></svg>`
	out, err := CleanSVG(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"script", "alert", "onload", "onclick", "evil", "foreignObject", "<a ", "<a>"} {
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
