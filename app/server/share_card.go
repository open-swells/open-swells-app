package main

// Share cards: the 1200x630 Open Graph images that link previews show for
// spot and region pages (iMessage, Slack, Discord, X, ...). Each is the
// current swell heatmap around the place, with land drawn over it. A
// spot's card is the map alone with the swells arriving at the beach, as
// the spot page's exposure map draws them; a region's adds a panel with
// its name and spot count.

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

const (
	cardW      = 1200
	cardH      = 630
	cardPanelW = 500
	cardPad    = 56
)

var (
	cardBG     = color.RGBA{0x0e, 0x0f, 0x12, 0xff}
	cardLand   = color.RGBA{0x26, 0x28, 0x2e, 0xff}
	cardWater  = color.RGBA{0x1b, 0x2a, 0x3d, 0xff}
	cardText   = color.RGBA{0xed, 0xee, 0xf1, 0xff}
	cardText2  = color.RGBA{0xa6, 0xaa, 0xb2, 0xff}
	cardText3  = color.RGBA{0x8b, 0x90, 0x99, 0xff}
	cardLine   = color.RGBA{0x2f, 0x31, 0x37, 0xff}
	cardAccent = color.RGBA{0x3b, 0x82, 0xf6, 0xff}
	cardBeach  = color.RGBA{0xf4, 0xa9, 0x67, 0xff}
	// The spot page's --swell and --swell-2: primary and secondary trains.
	cardSwell = map[string]color.RGBA{
		"primary":   {0x3b, 0x82, 0xf6, 0xff},
		"secondary": {0xe2, 0xb8, 0x6b, 0xff},
	}
	cardRatings = map[string]color.RGBA{
		"poor": {0xe3, 0xc3, 0x5a, 0xff},
		"fair": {0xa8, 0xdc, 0x7e, 0xff},
		"good": {0x2f, 0x9e, 0x5c, 0xff},
		"epic": {0xb4, 0x8c, 0xf0, 0xff},
	}
)

// cardRamp is the heatmap's color ramp (HEATMAP_COLORS in the pipeline,
// SWELL_BANDS in the map page) with each band's lower edge in feet.
var cardRamp = []struct {
	ft  string
	col color.RGBA
}{
	{"0", color.RGBA{0xa5, 0xd5, 0xf0, 0xff}}, {"2", color.RGBA{0x64, 0xa8, 0xe8, 0xff}},
	{"3", color.RGBA{0x31, 0x78, 0xd2, 0xff}}, {"5", color.RGBA{0x15, 0xa3, 0xa3, 0xff}},
	{"7", color.RGBA{0x2f, 0xb5, 0x4e, 0xff}}, {"8", color.RGBA{0xa3, 0xc5, 0x20, 0xff}},
	{"10", color.RGBA{0xf2, 0xce, 0x08, 0xff}}, {"13", color.RGBA{0xf5, 0x9a, 0x0b, 0xff}},
	{"16", color.RGBA{0xea, 0x4b, 0x28, 0xff}}, {"20", color.RGBA{0xb3, 0x12, 0x12, 0xff}},
	{"26", color.RGBA{0x4a, 0x0a, 0x1e, 0xff}}, {"33", color.RGBA{0x9b, 0x3f, 0xd9, 0xff}},
}

// The cards set text in Inter, the site's typeface (theme.css
// --font-sans), at the weights the site uses: 400, 500 and 600. The files
// in fonts/ are the Inter 4.1 release's static TTFs cut down to Latin
// with pyftsubset (fonts/README.md); Inter is under the SIL Open Font
// License (fonts/LICENSE.txt).
var (
	//go:embed fonts/Inter-Regular.ttf
	interRegular []byte
	//go:embed fonts/Inter-Medium.ttf
	interMedium []byte
	//go:embed fonts/Inter-SemiBold.ttf
	interSemiBold []byte
)

var (
	cardFontsOnce sync.Once
	cardFonts     struct{ regular, medium, bold *opentype.Font }
)

func loadCardFonts() {
	cardFontsOnce.Do(func() {
		parse := func(b []byte) *opentype.Font {
			f, err := opentype.Parse(b)
			if err != nil {
				panic(err) // embedded at build time; a bad file fails every test
			}
			return f
		}
		cardFonts.regular = parse(interRegular)
		cardFonts.medium = parse(interMedium)
		cardFonts.bold = parse(interSemiBold) // the site's bold is 600
	})
}

func cardFace(f *opentype.Font, size float64) font.Face {
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(err) // the embedded fonts parsed in loadCardFonts
	}
	return face
}

// cardPanel is the card's text, top to bottom. Empty fields are skipped.
type cardPanel struct {
	Title, Subtitle string
	Label           string // small caps line above Big, e.g. "SURF NOW"
	Big             string // e.g. "3–4 ft"
	Rating          string // poor/fair/good/epic chip beside Big
	Lines           []string
	Footer          string
}

type cardMarker struct {
	Lon, Lat float64
	R        float64 // radius, px
}

// renderCard draws the map (heatmap, land, markers) under the panel and
// encodes the card as a JPEG: link previews in some messengers drop images
// over a few hundred KB, and the heatmap's gradients compress well.
func renderCard(frame *heatmapFrame, view mercView, panel cardPanel, markers []cardMarker) ([]byte, error) {
	loadCardFonts()
	img := drawMapImage(frame, view, markers)

	// The panel: near-opaque, so text reads over any map.
	draw.Draw(img, image.Rect(0, 0, cardPanelW, cardH), image.NewUniform(color.NRGBA{0x0e, 0x0f, 0x12, 0xfa}), image.Point{}, draw.Over)
	draw.Draw(img, image.Rect(cardPanelW, 0, cardPanelW+1, cardH), image.NewUniform(cardLine), image.Point{}, draw.Src)
	// Drawn into the panel's own sub-image, so nothing spills onto the map.
	drawCardPanel(img.SubImage(image.Rect(0, 0, cardPanelW, cardH)).(*image.RGBA), panel)
	return encodeJPEG(img)
}

// cardSwellArrow is a swell train for a spot card: its kind ("primary",
// "secondary") and its outline in the exposure map's 100x100 viewBox
// (swellArrowShape), centered on the beach, and its label, e.g.
// "2.3ft at 14s".
type cardSwellArrow struct {
	Kind   string
	Points [][2]float64
	Label  string
}

// renderSpotCard is the spot's card: the heatmap and land with the swells
// arriving at the beach, drawn as the spot page's exposure map draws them
// (its square scaled to most of the card's height, leaving room past a
// full-length arrow's tail for its label), each labeled with the train's
// height and period. The preview's title says where it is.
func renderSpotCard(frame *heatmapFrame, view mercView, lon, lat float64, arrows []cardSwellArrow) ([]byte, error) {
	img := drawMapImage(frame, view, nil)
	cx, cy := view.Pixel(lon, lat)
	unit := 0.8 * float64(view.H) / 100
	// The page strokes at 1.5px on a ~216px square.
	stroke := 1.5 * float64(view.H) / 216
	for _, a := range arrows {
		pts := make([][2]float64, len(a.Points))
		for i, p := range a.Points {
			pts[i] = [2]float64{cx + (p[0]-50)*unit, cy + (p[1]-50)*unit}
		}
		fillPolygon(img, outsetPolygon(pts, stroke), color.RGBA{0xff, 0xff, 0xff, 0xff})
		fillPolygon(img, pts, cardSwell[a.Kind])
	}
	r := 2.2 * unit
	fillCircle(img, cx, cy, r+stroke, cardBG)
	fillCircle(img, cx, cy, r, cardBeach)
	drawArrowLabels(img, cx, cy, unit, arrows)
	return encodeJPEG(img)
}

// drawArrowLabels puts each arrow's label in a dark pill just past its
// tail, out along the swell's bearing, kept on the card and clear of the
// labels already placed.
func drawArrowLabels(img *image.RGBA, cx, cy, unit float64, arrows []cardSwellArrow) {
	loadCardFonts()
	face := cardFace(cardFonts.bold, 30)
	const padX, padY, gap, dotR = 16, 10, 12, 7
	asc := face.Metrics().Ascent.Ceil()
	textH := asc + face.Metrics().Descent.Ceil()
	bounds := img.Bounds()
	var placed []image.Rectangle
	for _, a := range arrows {
		if a.Label == "" || len(a.Points) < 2 {
			continue
		}
		// The tail's middle, and the bearing out from the beach.
		tx := cx + ((a.Points[0][0]+a.Points[1][0])/2-50)*unit
		ty := cy + ((a.Points[0][1]+a.Points[1][1])/2-50)*unit
		dx, dy := tx-cx, ty-cy
		if l := math.Hypot(dx, dy); l > 0 {
			dx, dy = dx/l, dy/l
		}
		w := 2*padX + 2*dotR + 10 + measure(face, a.Label)
		h := 2*padY + textH
		// Far enough along the bearing that the pill's nearest edge
		// clears the tail.
		reach := gap + math.Abs(dx)*float64(w)/2 + math.Abs(dy)*float64(h)/2
		area := bounds.Inset(16)
		at := func(along, across float64) image.Rectangle {
			mx := tx + dx*(reach+along) - dy*across
			my := ty + dy*(reach+along) + dx*across
			return image.Rect(int(mx)-w/2, int(my)-h/2, int(mx)-w/2+w, int(my)-h/2+h)
		}
		clear := func(r image.Rectangle) bool {
			if !r.In(area) {
				return false
			}
			for _, other := range placed {
				if r.Overlaps(other.Inset(-8)) {
					return false
				}
			}
			return true
		}
		// Past the tail if that's clear; else further out along the
		// bearing, then off to either side, so a label never lands back
		// on the arrows.
		box := keepInside(at(0, 0), area)
		if !clear(box) {
			found := false
			for step := 1; step <= 12 && !found; step++ {
				d := float64(step) * 16
				for _, c := range [][2]float64{{d, 0}, {0, d}, {0, -d}, {d, d}, {d, -d}} {
					if r := at(c[0], c[1]); clear(r) {
						box, found = r, true
						break
					}
				}
			}
		}
		placed = append(placed, box)
		fillRoundRect(img, box, float64(h)/2, color.NRGBA{0x0e, 0x0f, 0x12, 0xe6})
		fillCircle(img, float64(box.Min.X+padX+dotR), float64(box.Min.Y+box.Max.Y)/2, dotR, cardSwell[a.Kind])
		drawText(img, face, cardText, box.Min.X+padX+2*dotR+10, box.Min.Y+padY+asc, a.Label)
	}
}

// keepInside moves r, unchanged in size, to lie within area.
func keepInside(r, area image.Rectangle) image.Rectangle {
	if r.Min.X < area.Min.X {
		r = r.Add(image.Pt(area.Min.X-r.Min.X, 0))
	}
	if r.Max.X > area.Max.X {
		r = r.Add(image.Pt(area.Max.X-r.Max.X, 0))
	}
	if r.Min.Y < area.Min.Y {
		r = r.Add(image.Pt(0, area.Min.Y-r.Min.Y))
	}
	if r.Max.Y > area.Max.Y {
		r = r.Add(image.Pt(0, area.Max.Y-r.Max.Y))
	}
	return r
}

// fillRoundRect draws a rounded rectangle with corner radius rad.
func fillRoundRect(img *image.RGBA, r image.Rectangle, rad float64, col color.Color) {
	x0, y0, x1, y1 := float64(r.Min.X), float64(r.Min.Y), float64(r.Max.X), float64(r.Max.Y)
	var pts [][2]float64
	corner := func(cx, cy, start float64) {
		for i := 0; i <= 8; i++ {
			a := (start + 90*float64(i)/8) * math.Pi / 180
			pts = append(pts, [2]float64{cx + rad*math.Cos(a), cy + rad*math.Sin(a)})
		}
	}
	corner(x1-rad, y0+rad, 270)
	corner(x1-rad, y1-rad, 0)
	corner(x0+rad, y1-rad, 90)
	corner(x0+rad, y0+rad, 180)
	fillPolygon(img, pts, col)
}

// renderMap is a card's map alone, any size: the region pages' map.
func renderMap(frame *heatmapFrame, view mercView, markers []cardMarker) ([]byte, error) {
	return encodeJPEG(drawMapImage(frame, view, markers))
}

// drawMapImage draws the heatmap, land and the markers into an image the
// size of view.
func drawMapImage(frame *heatmapFrame, view mercView, markers []cardMarker) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, view.W, view.H))
	draw.Draw(img, img.Bounds(), image.NewUniform(cardWater), image.Point{}, draw.Src)
	if frame != nil {
		heat := frame.Render(view, false)
		draw.DrawMask(img, img.Bounds(), heat, image.Point{}, image.NewUniform(color.Alpha{0xeb}), image.Point{}, draw.Over)
	}
	landStore.Draw(img, view, cardLand)
	outline := math.Max(1.5, 0.35*markerRadius(markers))
	for _, m := range markers {
		x, y := view.Pixel(m.Lon, m.Lat)
		fillCircle(img, x, y, m.R+outline, cardBG)
	}
	for _, m := range markers {
		x, y := view.Pixel(m.Lon, m.Lat)
		fillCircle(img, x, y, m.R, cardBeach)
	}
	return img
}

func markerRadius(markers []cardMarker) float64 {
	if len(markers) == 0 {
		return 0
	}
	return markers[0].R
}

func encodeJPEG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func drawCardPanel(img *image.RGBA, p cardPanel) {
	textW := cardPanelW - 2*cardPad
	y := 84

	brand := cardFace(cardFonts.bold, 22)
	drawText(img, brand, cardAccent, cardPad, y, "OPENSWELLS")
	drawText(img, cardFace(cardFonts.medium, 22), cardText3, cardPad+measure(brand, "OPENSWELLS")+12, y, "SURF FORECAST")
	y += 34

	// Title: as large as fits in two lines.
	if p.Title != "" {
		size := 58.0
		var lines []string
		var face font.Face
		for ; size >= 34; size -= 4 {
			face = cardFace(cardFonts.bold, size)
			lines = wrapText(face, p.Title, textW)
			if len(lines) <= 2 && widest(face, lines) <= textW {
				break
			}
		}
		if len(lines) > 2 {
			lines = lines[:2]
			lines[1] += "…"
		}
		for _, line := range lines {
			y += int(size * 1.08)
			drawText(img, face, cardText, cardPad, y, truncateText(face, line, textW))
		}
	}
	if p.Subtitle != "" {
		face := cardFace(cardFonts.regular, 24)
		y += 40
		drawText(img, face, cardText2, cardPad, y, truncateText(face, p.Subtitle, textW))
	}

	y += 34
	draw.Draw(img, image.Rect(cardPad, y, cardPanelW-cardPad, y+1), image.NewUniform(cardLine), image.Point{}, draw.Src)

	if p.Label != "" {
		y += 44
		drawText(img, cardFace(cardFonts.bold, 18), cardText3, cardPad, y, p.Label)
	}
	if p.Big != "" {
		chip := cardFace(cardFonts.bold, 22)
		label := strings.ToUpper(p.Rating)
		_, hasChip := cardRatings[p.Rating]
		chipW := 0
		if hasChip {
			chipW = measure(chip, label) + 28 + 20
		}
		// The value and its rating chip share one line.
		var big font.Face
		for size := 72.0; ; size -= 6 {
			big = cardFace(cardFonts.bold, size)
			if size <= 42 || measure(big, p.Big)+chipW <= textW {
				break
			}
		}
		y += 78
		drawText(img, big, cardText, cardPad, y, truncateText(big, p.Big, textW-chipW))
		if col, ok := cardRatings[p.Rating]; ok {
			x := cardPad + measure(big, truncateText(big, p.Big, textW-chipW)) + 20
			w := chipW - 20
			draw.Draw(img, image.Rect(x, y-46, x+w, y-10), image.NewUniform(col), image.Point{}, draw.Src)
			drawText(img, chip, cardBG, x+14, y-20, label)
		}
	}
	lineFace := cardFace(cardFonts.regular, 25)
	for _, line := range p.Lines {
		// Lines stop clear of the legend; a two-line title leaves room
		// for fewer.
		if y+40 > cardH-120 {
			break
		}
		y += 40
		drawText(img, lineFace, cardText2, cardPad, y, truncateText(lineFace, line, textW))
	}

	// The legend and footer sit on the bottom edge.
	drawCardLegend(img, cardPad, cardH-92, textW)
	if p.Footer != "" {
		face := cardFace(cardFonts.medium, 20)
		drawText(img, face, cardText3, cardPad, cardH-40, truncateText(face, p.Footer, textW))
	}
}

// drawCardLegend is the heatmap's color bands with their feet.
func drawCardLegend(img *image.RGBA, x, y, w int) {
	face := cardFace(cardFonts.medium, 15)
	band := float64(w) / float64(len(cardRamp))
	for i, b := range cardRamp {
		x0 := x + int(math.Round(float64(i)*band))
		x1 := x + int(math.Round(float64(i+1)*band))
		draw.Draw(img, image.Rect(x0, y, x1, y+8), image.NewUniform(b.col), image.Point{}, draw.Src)
		if i%2 == 0 {
			drawText(img, face, cardText3, x0, y+28, b.ft)
		}
	}
	drawText(img, face, cardText3, x+w-measure(face, "ft"), y+28, "ft")
}

func widest(face font.Face, lines []string) int {
	w := 0
	for _, l := range lines {
		w = max(w, measure(face, l))
	}
	return w
}

func drawText(img *image.RGBA, face font.Face, col color.Color, x, y int, s string) {
	d := font.Drawer{Dst: img, Src: image.NewUniform(col), Face: face, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

func measure(face font.Face, s string) int {
	return font.MeasureString(face, s).Ceil()
}

// wrapText breaks s into lines no wider than w, at spaces.
func wrapText(face font.Face, s string, w int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		next := word
		if line != "" {
			next = line + " " + word
		}
		if line != "" && measure(face, next) > w {
			lines = append(lines, line)
			line = word
			continue
		}
		line = next
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// truncateText shortens s with an ellipsis until it fits in w.
func truncateText(face font.Face, s string, w int) string {
	if measure(face, s) <= w {
		return s
	}
	runes := []rune(strings.TrimSuffix(s, "…"))
	for len(runes) > 0 {
		runes = runes[:len(runes)-1]
		t := strings.TrimRight(string(runes), " ,·") + "…"
		if measure(face, t) <= w {
			return t
		}
	}
	return ""
}

// fillCircle draws an antialiased disc, rasterizing only its own box
// (a region map draws hundreds).
func fillCircle(img *image.RGBA, cx, cy, r float64, col color.Color) {
	x0, y0 := int(math.Floor(cx-r))-1, int(math.Floor(cy-r))-1
	box := image.Rect(x0, y0, int(math.Ceil(cx+r))+2, int(math.Ceil(cy+r))+2)
	if !box.Overlaps(img.Bounds()) {
		return
	}
	ras := vector.NewRasterizer(box.Dx(), box.Dy())
	const k = 0.5523 // cubic Bézier circle constant
	x, y, rr := float32(cx-float64(x0)), float32(cy-float64(y0)), float32(r)
	kr := float32(k * r)
	ras.MoveTo(x+rr, y)
	ras.CubeTo(x+rr, y+kr, x+kr, y+rr, x, y+rr)
	ras.CubeTo(x-kr, y+rr, x-rr, y+kr, x-rr, y)
	ras.CubeTo(x-rr, y-kr, x-kr, y-rr, x, y-rr)
	ras.CubeTo(x+kr, y-rr, x+rr, y-kr, x+rr, y)
	ras.ClosePath()
	// Rasterize into the disc's own mask; DrawMask clips it to the image
	// for discs partly off the edge.
	mask := image.NewAlpha(image.Rect(0, 0, box.Dx(), box.Dy()))
	ras.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	draw.DrawMask(img, box, image.NewUniform(col), image.Point{}, mask, image.Point{}, draw.Over)
}

// fillPolygon draws an antialiased polygon, rasterizing only its own box.
func fillPolygon(img *image.RGBA, pts [][2]float64, col color.Color) {
	if len(pts) < 3 {
		return
	}
	minX, minY, maxX, maxY := pts[0][0], pts[0][1], pts[0][0], pts[0][1]
	for _, p := range pts {
		minX, maxX = math.Min(minX, p[0]), math.Max(maxX, p[0])
		minY, maxY = math.Min(minY, p[1]), math.Max(maxY, p[1])
	}
	x0, y0 := int(math.Floor(minX))-1, int(math.Floor(minY))-1
	box := image.Rect(x0, y0, int(math.Ceil(maxX))+2, int(math.Ceil(maxY))+2)
	if !box.Overlaps(img.Bounds()) {
		return
	}
	ras := vector.NewRasterizer(box.Dx(), box.Dy())
	for i, p := range pts {
		x, y := float32(p[0]-float64(x0)), float32(p[1]-float64(y0))
		if i == 0 {
			ras.MoveTo(x, y)
		} else {
			ras.LineTo(x, y)
		}
	}
	ras.ClosePath()
	mask := image.NewAlpha(image.Rect(0, 0, box.Dx(), box.Dy()))
	ras.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	draw.DrawMask(img, box, image.NewUniform(col), image.Point{}, mask, image.Point{}, draw.Over)
}

// outsetPolygon grows a simple polygon by d on every side, mitered: the
// outline a stroke of width 2d paints outside it (SVG's paint-order:
// stroke, with the fill drawn back over the inner half).
func outsetPolygon(pts [][2]float64, d float64) [][2]float64 {
	n := len(pts)
	var area float64
	for i, p := range pts {
		q := pts[(i+1)%n]
		area += p[0]*q[1] - q[0]*p[1]
	}
	// With y down, a positive shoelace area winds clockwise on screen and
	// each edge's outward normal is (dy, -dx).
	sign := 1.0
	if area < 0 {
		sign = -1
	}
	normal := func(a, b [2]float64) (float64, float64) {
		dx, dy := b[0]-a[0], b[1]-a[1]
		l := math.Hypot(dx, dy)
		if l == 0 {
			return 0, 0
		}
		return sign * dy / l, -sign * dx / l
	}
	out := make([][2]float64, n)
	for i, p := range pts {
		n1x, n1y := normal(pts[(i+n-1)%n], p)
		n2x, n2y := normal(p, pts[(i+1)%n])
		mx, my := n1x+n2x, n1y+n2y
		l := math.Hypot(mx, my)
		if l < 1e-9 {
			out[i] = p
			continue
		}
		mx, my = mx/l, my/l
		// Miter length d/cos(half the turn), capped like SVG's default
		// miter limit of 4.
		k := d / math.Max(0.25, mx*n1x+my*n1y)
		out[i] = [2]float64{p[0] + mx*k, p[1] + my*k}
	}
	return out
}
