package board

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"regexp"
	"strings"
)

// Free drawing: a box whose label is a fenced SVG (```svg …) shows that SVG
// in the box's place, fitted to it, with no box drawn round it. The SVG is
// whatever the teacher (or Claude, in free draw) made, kept only after it is
// cleaned: drawing elements and SMIL animation, no script, no handlers, no
// links or url()s out of the drawing. A foreignObject may hold a little HTML
// (a button, some text), nothing else.
//
// Triggers: an element with data-trigger="name" is a button: a click on it
// starts every animation in the drawing whose data-anim names it (an
// animation that should wait for it says begin="indefinite"). The drawing
// never carries its own handlers: the click is a fixed Datastar expression
// written here, with the name (a plain word) the only thing taken from it.

// SvgMax is a fenced SVG's limit.
const SvgMax = 24000

var svgElems = map[string]bool{
	"svg": true, "g": true, "defs": true, "title": true, "desc": true, "symbol": true, "use": true,
	"path": true, "rect": true, "circle": true, "ellipse": true, "line": true, "polyline": true, "polygon": true,
	"text": true, "tspan": true, "textPath": true,
	"linearGradient": true, "radialGradient": true, "stop": true, "pattern": true, "clipPath": true, "mask": true, "marker": true,
	"animate": true, "animateTransform": true, "animateMotion": true, "set": true, "mpath": true,
	"foreignObject": true,
	"filter":        true, "feGaussianBlur": true, "feOffset": true, "feBlend": true, "feColorMatrix": true, "feMerge": true, "feMergeNode": true, "feDropShadow": true,
}

var svgAttrs = map[string]bool{
	"viewBox": true, "preserveAspectRatio": true, "width": true, "height": true, "x": true, "y": true, "x1": true, "y1": true, "x2": true, "y2": true,
	"cx": true, "cy": true, "r": true, "rx": true, "ry": true, "d": true, "points": true, "pathLength": true,
	"fill": true, "fill-opacity": true, "fill-rule": true, "stroke": true, "stroke-width": true, "stroke-opacity": true, "stroke-linecap": true,
	"stroke-linejoin": true, "stroke-dasharray": true, "stroke-dashoffset": true, "stroke-miterlimit": true, "opacity": true, "color": true,
	"transform": true, "transform-origin": true, "style": true, "class": true, "id": true,
	"font-family": true, "font-size": true, "font-weight": true, "font-style": true, "text-anchor": true, "dominant-baseline": true, "letter-spacing": true,
	"dx": true, "dy": true, "rotate": true, "textLength": true, "startOffset": true,
	"offset": true, "stop-color": true, "stop-opacity": true, "gradientUnits": true, "gradientTransform": true, "fx": true, "fy": true, "spreadMethod": true,
	"patternUnits": true, "patternContentUnits": true, "patternTransform": true, "clipPathUnits": true, "maskUnits": true, "maskContentUnits": true,
	"clip-path": true, "mask": true, "marker-start": true, "marker-mid": true, "marker-end": true,
	"markerWidth": true, "markerHeight": true, "refX": true, "refY": true, "orient": true, "markerUnits": true,
	"attributeName": true, "attributeType": true, "from": true, "to": true, "by": true, "values": true, "keyTimes": true, "keySplines": true,
	"calcMode": true, "dur": true, "begin": true, "end": true, "repeatCount": true, "repeatDur": true, "fill-mode": true, "additive": true,
	"accumulate": true, "type": true, "path": true, "keyPoints": true, "restart": true, "min": true, "max": true,
	"stdDeviation": true, "in": true, "in2": true, "result": true, "mode": true, "flood-color": true, "flood-opacity": true, "filter": true,
	"href": true, "xlink:href": true, "visibility": true, "display": true, "vector-effect": true, "paint-order": true, "mix-blend-mode": true,
}

// the HTML a foreignObject may hold, and its attributes
var htmlElems = map[string]bool{
	"button": true, "div": true, "span": true, "p": true, "b": true, "i": true, "em": true, "strong": true,
	"code": true, "small": true, "br": true, "sub": true, "sup": true,
}
var htmlAttrs = map[string]bool{"class": true, "style": true, "title": true}

// a trigger's or an animation's name
var triggerName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// a url() may only name something in the same drawing (#id)
var badURL = regexp.MustCompile(`(?i)url\(\s*['"]?\s*[^#'"\s)]`)
var badStyle = regexp.MustCompile(`(?i)expression|javascript:|@import|behavior|position|url\(`)

// CleanSVG keeps what is safe of an SVG document; it errs when nothing of
// one is left or it is too big.
func CleanSVG(src string) (string, error) {
	if len(src) > SvgMax {
		return "", errors.New("the svg is too big")
	}
	dec := xml.NewDecoder(strings.NewReader(src))
	dec.Strict = false
	var out bytes.Buffer
	enc := xml.NewEncoder(&out)
	skip := 0 // inside an element we drop
	root := false
	var stack []string // the open elements kept
	inFO := 0          // inside a foreignObject: HTML, not SVG
	inHTML := false    // inside the HTML of one (its first element names the namespace)
	htmlDepth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", errors.New("the svg does not parse")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			html := inFO > 0 && htmlElems[name]
			if skip > 0 || !(html || inFO == 0 && svgElems[name]) || (!root && name != "svg") {
				skip++
				continue
			}
			root = true
			el := xml.StartElement{Name: xml.Name{Local: name}}
			if html && !inHTML {
				el.Attr = append(el.Attr, xml.Attr{Name: xml.Name{Local: "xmlns"}, Value: "http://www.w3.org/1999/xhtml"})
			}
			trigger := ""
			for _, a := range t.Attr {
				an := a.Name.Local
				if a.Name.Space == "xlink" || a.Name.Space == "http://www.w3.org/1999/xlink" {
					an = "xlink:" + an
				}
				v := a.Value
				if an == "data-trigger" || an == "data-anim" {
					if v = strings.TrimSpace(v); triggerName.MatchString(v) {
						el.Attr = append(el.Attr, xml.Attr{Name: xml.Name{Local: an}, Value: v})
						if an == "data-trigger" {
							trigger = v
						}
					}
					continue
				}
				if html && !htmlAttrs[an] || !html && !svgAttrs[an] || strings.HasPrefix(strings.ToLower(an), "on") {
					continue
				}
				if (an == "href" || an == "xlink:href") && !strings.HasPrefix(v, "#") {
					continue // only the drawing's own parts
				}
				if badURL.MatchString(v) || (an == "style" && badStyle.MatchString(v)) {
					continue
				}
				el.Attr = append(el.Attr, xml.Attr{Name: xml.Name{Local: an}, Value: v})
			}
			if trigger != "" {
				el.Attr = append(el.Attr,
					xml.Attr{Name: xml.Name{Local: "data-on:pointerdown__stop"}, Value: "evt.preventDefault()"},
					xml.Attr{Name: xml.Name{Local: "data-on:click__stop"}, Value: "el.closest('svg.art').querySelectorAll('[data-anim=" + trigger + "]').forEach(a => a.beginElement && a.beginElement())"})
			}
			if name == "svg" && len(out.Bytes()) == 0 {
				el.Attr = append(el.Attr, xml.Attr{Name: xml.Name{Local: "xmlns"}, Value: "http://www.w3.org/2000/svg"})
			}
			if err := enc.EncodeToken(el); err != nil {
				return "", err
			}
			stack = append(stack, name)
			if name == "foreignObject" {
				inFO++
			}
			if html {
				inHTML = true
				htmlDepth++
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			name := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if name == "foreignObject" {
				inFO--
			} else if inFO > 0 && htmlElems[name] {
				if htmlDepth--; htmlDepth == 0 {
					inHTML = false
				}
			}
			if err := enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: name}}); err != nil {
				return "", err
			}
		case xml.CharData:
			if skip == 0 && root {
				if err := enc.EncodeToken(t); err != nil {
					return "", err
				}
			}
		}
	}
	if err := enc.Flush(); err != nil {
		return "", err
	}
	if !root || out.Len() == 0 {
		return "", errors.New("no svg in it")
	}
	return out.String(), nil
}
