package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRegionPathAndSlug(t *testing.T) {
	for _, tc := range []struct {
		region string
		want   string
	}{
		{"USA, California, San Diego County", "USA|California|San Diego County"},
		{"Puerto Rico", "Puerto Rico"},
		{"", ""},
	} {
		if got := strings.Join(regionPath(tc.region), "|"); got != tc.want {
			t.Errorf("regionPath(%q) = %q, want %q", tc.region, got, tc.want)
		}
	}
	if got := regionSlug("Pipes - San elijo, Réunion"); got != "pipes-san-elijo-réunion" {
		t.Errorf("regionSlug = %q", got)
	}
}

// The region hierarchy is read straight from spots.json, so the data must
// be in "Country, Region, Area" form: no padded or empty levels, no runs
// of spaces, and no postal-style "Town, CA, USA" (which would make a
// one-spot country of the town).
func TestSpotRegionsAreClean(t *testing.T) {
	store, err := NewSpotStore(filepath.Join("..", "data", "spots.json"))
	if err != nil {
		t.Fatal(err)
	}
	countries := map[string]bool{}
	for _, s := range store.All() {
		if p := regionPath(s.Region); len(p) > 0 {
			countries[p[0]] = true
		}
	}
	for _, s := range store.All() {
		parts := strings.Split(s.Region, ",")
		for i, part := range parts {
			want := strings.TrimSpace(part)
			if i > 0 {
				want = " " + want
			}
			if want == "" || want == " " || part != want || strings.Contains(part, "  ") {
				t.Errorf("%s: messy region %q", s.ID, s.Region)
				break
			}
		}
		if p := regionPath(s.Region); len(p) > 1 && countries[p[len(p)-1]] {
			t.Errorf("%s: region %q is written country last", s.ID, s.Region)
		}
	}
}

func TestRegionTreeFromSpots(t *testing.T) {
	store, err := NewSpotStore(filepath.Join("..", "data", "spots.json"))
	if err != nil {
		t.Fatal(err)
	}
	tree := NewRegionTree(store.All())
	if err := tree.checkUniqueNames(); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, c := range tree.Root.Children {
		total += c.SpotCount
	}
	if total != store.Count() {
		t.Errorf("countries hold %d spots, want %d", total, store.Count())
	}
	sd, ok := tree.Get("usa/california/san-diego-county")
	if !ok {
		t.Fatal("no San Diego County region")
	}
	if sd.URL() != "/surf-forecasts/usa/california/san-diego-county" || sd.DisplayName() != "San Diego County, California" {
		t.Errorf("San Diego County: url %q, name %q", sd.URL(), sd.DisplayName())
	}
	if n, ok := tree.ForSpot("blacks-beach"); !ok || n.Slug != "usa/california/san-diego-county" {
		t.Errorf("blacks-beach region = %+v", n)
	}
}

func TestRegionPagesRender(t *testing.T) {
	gin.SetMode(gin.TestMode)
	spots := []Spot{
		{ID: "a-break", Name: "A Break", Region: "Testland, North", Lat: 10, Lon: 20},
		{ID: "b-break", Name: "B Break", Region: "Testland, North", Lat: 10.1, Lon: 20.1},
		{ID: "c-break", Name: "C Break", Region: "Testland, South", Lat: 9, Lon: 20},
	}
	tree := NewRegionTree(spots)
	tmpl := loadTemplates(filepath.Join("..", "web", "templates"))
	router := gin.New()
	handler := regionHandler(tmpl, t.TempDir(), "https://openswells.com", tree)
	router.GET(regionIndexPath, handler)
	router.GET(regionIndexPath+"/*path", handler)

	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		router.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
		return r
	}
	country := get("/surf-forecasts/testland")
	if country.Code != http.StatusOK {
		t.Fatalf("country status %d", country.Code)
	}
	for _, want := range []string{
		"<title>Testland Surf Forecast &amp; Surf Report | OpenSwells</title>",
		`<link rel="canonical" href="https://openswells.com/surf-forecasts/testland">`,
		`href="/surf-forecasts/testland/north"`,
		`href="/surf-spots/a-break"`,
		`content="https://openswells.com/surf-forecasts/testland/card.jpg`,
		`"@type":"BreadcrumbList"`,
	} {
		if !strings.Contains(country.Body.String(), want) {
			t.Errorf("country page is missing %q", want)
		}
	}
	if strings.Contains(country.Body.String(), "noindex") {
		t.Error("a three-spot region should be indexable")
	}
	if south := get("/surf-forecasts/testland/south"); !strings.Contains(south.Body.String(), `content="noindex, follow"`) {
		t.Error("a one-spot region should be noindex")
	}
	if index := get("/surf-forecasts"); index.Code != http.StatusOK || !strings.Contains(index.Body.String(), `href="/surf-forecasts/testland"`) {
		t.Errorf("index status %d", index.Code)
	}
	if r := get("/surf-forecasts/Testland"); r.Code != http.StatusMovedPermanently || r.Header().Get("Location") != "/surf-forecasts/testland" {
		t.Errorf("mixed case: %d %q", r.Code, r.Header().Get("Location"))
	}
	if r := get("/surf-forecasts/nowhere"); r.Code != http.StatusNotFound {
		t.Errorf("unknown region status %d", r.Code)
	}
	for _, path := range []string{"/surf-forecasts/testland/map.jpg", "/surf-forecasts/map.jpg"} {
		if r := get(path); r.Code != http.StatusOK || r.Header().Get("Content-Type") != "image/jpeg" {
			t.Errorf("%s: %d %q", path, r.Code, r.Header().Get("Content-Type"))
		}
	}
	if !strings.Contains(country.Body.String(), `src="/surf-forecasts/testland/map.jpg`) {
		t.Error("country page has no map")
	}
	card := get("/surf-forecasts/testland/north/card.jpg")
	if card.Code != http.StatusOK || card.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("card: %d %q", card.Code, card.Header().Get("Content-Type"))
	}
	img, err := jpeg.Decode(bytes.NewReader(card.Body.Bytes()))
	if err != nil || img.Bounds() != image.Rect(0, 0, cardW, cardH) {
		t.Errorf("card image: %v %v", err, img)
	}
}

func TestSpotTextFromBeach(t *testing.T) {
	// Land blocks the facing (NW); the open water runs SSW to W.
	b := beach{Facing: 321, HasFacing: true, OpenWater: [][2]float64{{202, 274}}}
	text := buildSpotText(spotTextInput{
		Spot: Spot{Name: "Test Point", Region: "Indonesia, Bali"}, Name: "Test Point", Beach: b,
		Report:    SpotReport{PrimaryHeight: "3.3", PrimaryPeriod: "13.2", PrimaryDegrees: "200", WindSpeed: "5", WindDir: "140", HasWind: true},
		HasReport: true,
		Now:       HourlyCondition{Surf: "3–5ft", Rating: "fair", WindMph: 5, WindDir: 140, HasWind: true}, HasNow: true,
		Today: "2026-11-24",
		Days: []ForecastSummary{
			{Date: "2026-11-24", WaveHeight: "3–5ft", Condition: "fair", Score: 40},
			{Date: "2026-11-29", WaveHeight: "4–6ft", Condition: "good", Score: 70},
		},
	})
	about := strings.Join(text.About, " ")
	for _, want := range []string{"Test Point is a surf spot in Indonesia, Bali.", "faces NW (321°)", "Land shelters it from swell straight out", "Offshore wind here blows from the SE"} {
		if !strings.Contains(about, want) {
			t.Errorf("about %q is missing %q", about, want)
		}
	}
	outlook := strings.Join(text.Outlook, " ")
	for _, want := range []string{"3–5 ft surf, rated fair", "3.3ft, 13-second SSW swell", "5 mph from the SE, offshore", "Sunday, November 29, with 4–6 ft rated good"} {
		if !strings.Contains(outlook, want) {
			t.Errorf("outlook %q is missing %q", outlook, want)
		}
	}
	if !strings.HasPrefix(text.Summary, "3–5 ft and fair right now; best day Sun with 4–6 ft (good).") {
		t.Errorf("summary = %q", text.Summary)
	}
}

func TestSpotTextEdgeCases(t *testing.T) {
	facing := beach{Facing: 270, HasFacing: true}
	// A run whose first day is tomorrow never calls it "today"; one day
	// and one neighbor read in the singular.
	text := buildSpotText(spotTextInput{
		Spot: Spot{Name: "X"}, Name: "X", Beach: facing, Today: "2026-11-24",
		Days:   []ForecastSummary{{Date: "2026-11-25", WaveHeight: "2–3ft", Condition: "fair", Score: 40}},
		Nearby: []NearbySpot{{Name: "Y", Distance: 1.2}},
	})
	all := strings.Join(append(text.About, text.Outlook...), " ")
	for _, want := range []string{"the forecast's one remaining day", "Wednesday, November 25", "The closest other break with a forecast is Y (1.2 mi)."} {
		if !strings.Contains(all, want) {
			t.Errorf("text %q is missing %q", all, want)
		}
	}
	if strings.Contains(all, "today") || strings.Contains(all, "1 days") {
		t.Errorf("text %q", all)
	}
	// All poor: name the highest-rated day, not "the biggest".
	text = buildSpotText(spotTextInput{
		Spot: Spot{Name: "X"}, Name: "X", Today: "2026-11-24",
		Days: []ForecastSummary{
			{Date: "2026-11-24", WaveHeight: "4–5ft", Condition: "poor", Score: 10},
			{Date: "2026-11-25", WaveHeight: "2–3ft", Condition: "poor", Score: 20},
		},
	})
	if p := strings.Join(text.Outlook, " "); !strings.Contains(p, "the highest-rated day is Wednesday, November 25, with 2–3 ft") || strings.Contains(p, "biggest") {
		t.Errorf("all-poor outlook = %q", p)
	}
	// A hand-tuned window makes no "most squarely" claim.
	text = buildSpotText(spotTextInput{Spot: Spot{Name: "X"}, Name: "X", Beach: beach{Facing: 250, HasFacing: true, Window: []float64{200, 300}}})
	if a := strings.Join(text.About, " "); strings.Contains(a, "squarely") || !strings.Contains(a, "swell window from the SSW (200°)") {
		t.Errorf("window about = %q", a)
	}
	// Countries that take an article.
	if got := (&RegionNode{Name: "USA", Path: []string{"USA"}}).ProseName(); got != "the USA" {
		t.Errorf("ProseName = %q", got)
	}
}

func TestLonInterval(t *testing.T) {
	for _, tc := range []struct {
		lons     []float64
		min, max float64
	}{
		{[]float64{-117, -118, -116}, -118, -116},
		{[]float64{179, -179, 178}, 178, 181}, // Fiji, across the antimeridian
		{[]float64{179, 2, 90}, 2, 179},       // the short way round is not always across
	} {
		var spots []Spot
		for _, lon := range tc.lons {
			spots = append(spots, Spot{Lon: lon})
		}
		if lo, hi := lonInterval(spots); abs(lo-tc.min) > 1e-9 || abs(hi-tc.max) > 1e-9 {
			t.Errorf("lonInterval(%v) = %v, %v; want %v, %v", tc.lons, lo, hi, tc.min, tc.max)
		}
	}
}

func TestHeatmapSamplesAtPixelCenters(t *testing.T) {
	// A 4-column global frame: samples at 0, 90, 180, 270 degrees.
	img := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	for x := 0; x < 4; x++ {
		for y := 0; y < 2; y++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(60 * x), 0, 0, 255})
		}
	}
	frame := &heatmapFrame{layers: []heatmapLayer{newHeatmapLayer(img, lonLatBounds{West: 0, East: 270, North: 10, South: -10}, true)}}
	at := func(lon float64) uint8 {
		v := viewAt(lon, 0, 0.001, 1, 1, 0.5, 0.5)
		return frame.Render(v, false).RGBAAt(0, 0).R
	}
	if got := at(90); got < 58 || got > 62 {
		t.Errorf("at 90°E red = %d, want 60", got)
	}
	// Past the last column the global frame wraps back to the first.
	if got := at(315); got < 88 || got > 92 {
		t.Errorf("at 315°E red = %d, want 90 (halfway from 180 back to 0)", got)
	}
}

func TestSnapshotsExpire(t *testing.T) {
	store := &spotSnapshotStore{data: SnapshotSet{"a": {Rating: "fair"}}, builtAt: time.Now()}
	if _, ok := store.Get("a"); !ok {
		t.Fatal("fresh snapshot missing")
	}
	store.builtAt = time.Now().Add(-snapshotMaxAge - time.Minute)
	if store.Current() != nil {
		t.Error("stale snapshots still served")
	}
}

func TestImageCacheEvictsOneAndCoalesces(t *testing.T) {
	c := newImageCache(2)
	renders := 0
	for _, k := range []string{"a", "b", "c", "c"} {
		if _, err := c.getOrRender(k, func() ([]byte, error) { renders++; return []byte(k), nil }); err != nil {
			t.Fatal(err)
		}
	}
	if renders != 3 || len(c.data) != 2 {
		t.Errorf("renders %d, entries %d", renders, len(c.data))
	}
}

func TestFillCircleAtImageEdge(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 20, 20))
	for _, p := range [][2]float64{{0, 0}, {19.5, 10}, {-3, -3}, {25, 25}} {
		fillCircle(img, p[0], p[1], 5, color.RGBA{255, 0, 0, 255})
	}
	if img.RGBAAt(0, 0).R != 255 {
		t.Error("disc at the corner not drawn")
	}
}

func TestMercatorViewAndLand(t *testing.T) {
	v := viewAt(-117.27, 32.85, 8, 400, 200, 0.5, 0.5)
	x, y := v.Pixel(-117.27, 32.85)
	if abs(x-200) > 1e-6 || abs(y-100) > 1e-6 {
		t.Errorf("center at (%v, %v)", x, y)
	}
	if lat := v.LatAt(100); abs(lat-32.85) > 1e-6 {
		t.Errorf("LatAt(center) = %v", lat)
	}
	// A square island in the middle of the view is filled; the sea around it is not.
	store := &LandStore{rings: []landRing{{minLon: -118, maxLon: -116.5, minLat: 32, maxLat: 33.5,
		pts: []float32{-118, 32, -116.5, 32, -116.5, 33.5, -118, 33.5}}}}
	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	store.Draw(img, v, color.RGBA{255, 0, 0, 255})
	if img.RGBAAt(200, 100).R != 255 || img.RGBAAt(5, 5).R != 0 {
		t.Error("land fill is misplaced")
	}
	// A ring far bigger than the canvas still fills it.
	huge := &LandStore{rings: []landRing{{minLon: -170, maxLon: -50, minLat: 10, maxLat: 70,
		pts: []float32{-170, 10, -50, 10, -50, 70, -170, 70}}}}
	img = image.NewRGBA(image.Rect(0, 0, 400, 200))
	huge.Draw(img, v, color.RGBA{255, 0, 0, 255})
	if img.RGBAAt(5, 5).R != 255 || img.RGBAAt(395, 195).R != 255 {
		t.Error("clipped continent does not fill the view")
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
