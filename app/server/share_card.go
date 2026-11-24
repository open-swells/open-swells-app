package main

// Share cards: the 1200x630 Open Graph images that link previews show for
// spot and region pages (iMessage, Slack, Discord, X, ...). Each is the
// current swell heatmap around the place, with land drawn over it, and a
// panel with the name and the conditions right now.

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gomedium"
	"golang.org/x/image/font/gofont/goregular"
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
	cardBG      = color.RGBA{0x0e, 0x0f, 0x12, 0xff}
	cardLand    = color.RGBA{0x26, 0x28, 0x2e, 0xff}
	cardWater   = color.RGBA{0x1b, 0x2a, 0x3d, 0xff}
	cardText    = color.RGBA{0xed, 0xee, 0xf1, 0xff}
	cardText2   = color.RGBA{0xa6, 0xaa, 0xb2, 0xff}
	cardText3   = color.RGBA{0x8b, 0x90, 0x99, 0xff}
	cardLine    = color.RGBA{0x2f, 0x31, 0x37, 0xff}
	cardAccent  = color.RGBA{0x3b, 0x82, 0xf6, 0xff}
	cardBeach   = color.RGBA{0xf4, 0xa9, 0x67, 0xff}
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

var (
	cardFontsOnce sync.Once
	cardFonts     struct{ regular, medium, bold *opentype.Font }
)

func loadCardFonts() {
	cardFontsOnce.Do(func() {
		cardFonts.regular, _ = opentype.Parse(goregular.TTF)
		cardFonts.medium, _ = opentype.Parse(gomedium.TTF)
		cardFonts.bold, _ = opentype.Parse(gobold.TTF)
	})
}

func cardFace(f *opentype.Font, size float64) font.Face {
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(err) // the embedded Go fonts always parse
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

// cardArrow is a swell arriving at a point from a bearing (degrees true).
type cardArrow struct {
	Lon, Lat, From float64
}

// renderCard draws the map (heatmap, land, markers, arrow) under the
// panel and encodes the card as a JPEG: link previews in some messengers
// drop images over a few hundred KB, and the heatmap's gradients compress
// well.
func renderCard(frame *heatmapFrame, view mercView, panel cardPanel, markers []cardMarker, arrow *cardArrow) ([]byte, error) {
	loadCardFonts()
	img := drawMapImage(frame, view, markers, arrow)

	// The panel: near-opaque, so text reads over any map.
	draw.Draw(img, image.Rect(0, 0, cardPanelW, cardH), image.NewUniform(color.NRGBA{0x0e, 0x0f, 0x12, 0xfa}), image.Point{}, draw.Over)
	draw.Draw(img, image.Rect(cardPanelW, 0, cardPanelW+1, cardH), image.NewUniform(cardLine), image.Point{}, draw.Src)
	// Drawn into the panel's own sub-image, so nothing spills onto the map.
	drawCardPanel(img.SubImage(image.Rect(0, 0, cardPanelW, cardH)).(*image.RGBA), panel)
	return encodeJPEG(img)
}

// renderMap is a card's map alone, any size: the region pages' map.
func renderMap(frame *heatmapFrame, view mercView, markers []cardMarker) ([]byte, error) {
	return encodeJPEG(drawMapImage(frame, view, markers, nil))
}

// drawMapImage draws the heatmap, land, the swell arrow and the markers
// into an image the size of view.
func drawMapImage(frame *heatmapFrame, view mercView, markers []cardMarker, arrow *cardArrow) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, view.W, view.H))
	draw.Draw(img, img.Bounds(), image.NewUniform(cardWater), image.Point{}, draw.Src)
	if frame != nil {
		heat := frame.Render(view, false)
		draw.DrawMask(img, img.Bounds(), heat, image.Point{}, image.NewUniform(color.Alpha{0xeb}), image.Point{}, draw.Over)
	}
	landStore.Draw(img, view, cardLand)
	if arrow != nil {
		x, y := view.Pixel(arrow.Lon, arrow.Lat)
		drawSwellArrow(img, x, y, arrow.From)
	}
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

// drawSwellArrow draws a swell arriving at (x, y) from bearing from: the
// tail out toward the swell's source, the head stopping short of the spot.
func drawSwellArrow(img *image.RGBA, x, y, from float64) {
	rad := from * math.Pi / 180
	ux, uy := math.Sin(rad), -math.Cos(rad) // toward the source, screen axes
	px, py := -uy, ux                       // perpendicular
	at := func(along, across float64) (float32, float32) {
		return float32(x + ux*along + px*across), float32(y + uy*along + py*across)
	}
	shape := func(grow float64, col color.Color) {
		const tail, tip, headLen, headHalf, shaft = 150.0, 20.0, 34.0, 17.0, 5.0
		ras := vector.NewRasterizer(img.Bounds().Dx(), img.Bounds().Dy())
		pts := [][2]float64{
			{tip - grow, 0},
			{tip + headLen + grow*0.4, headHalf + grow},
			{tip + headLen + grow*0.4, shaft + grow},
			{tail + grow, shaft + grow},
			{tail + grow, -shaft - grow},
			{tip + headLen + grow*0.4, -shaft - grow},
			{tip + headLen + grow*0.4, -headHalf - grow},
		}
		for i, p := range pts {
			ax, ay := at(p[0], p[1])
			if i == 0 {
				ras.MoveTo(ax, ay)
			} else {
				ras.LineTo(ax, ay)
			}
		}
		ras.ClosePath()
		ras.Draw(img, img.Bounds(), image.NewUniform(col), image.Point{})
	}
	shape(3, cardBG)
	shape(0, color.RGBA{0xff, 0xff, 0xff, 0xff})
}
