package main

import (
	"bytes"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCanonicalSiteURL(t *testing.T) {
	got, err := canonicalSiteURL("https://openswells.com/")
	if err != nil || got != "https://openswells.com" {
		t.Fatalf("canonicalSiteURL = %q, %v", got, err)
	}
	for _, invalid := range []string{"openswells.com", "ftp://openswells.com", "https://openswells.com/app"} {
		if _, err := canonicalSiteURL(invalid); err == nil {
			t.Errorf("canonicalSiteURL(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestSpotSEOIdentitiesAreUnique(t *testing.T) {
	store, err := NewSpotStore(filepath.Join("..", "data", "spots.json"))
	if err != nil {
		t.Fatal(err)
	}
	titles := make(map[string]string, store.Count())
	descriptions := make(map[string]string, store.Count())
	canonicals := make(map[string]string, store.Count())
	headings := make(map[string]string, store.Count())
	for _, spot := range store.All() {
		seo := spotSEOPage("https://openswells.com", store, spot)
		values := []struct {
			kind  string
			value string
			seen  map[string]string
		}{
			{"title", seo.Title, titles}, {"description", seo.Description, descriptions},
			{"canonical", seo.Canonical, canonicals}, {"H1", seo.OpenGraphTitle, headings},
		}
		for _, value := range values {
			if previous, exists := value.seen[value.value]; exists {
				t.Errorf("duplicate %s for %s and %s: %q", value.kind, previous, spot.ID, value.value)
			}
			value.seen[value.value] = spot.ID
		}
		if !strings.HasSuffix(seo.Canonical, "/surf-spots/"+spot.ID) {
			t.Errorf("canonical for %s = %q", spot.ID, seo.Canonical)
		}
	}
}

func TestSEOIndexAndRobots(t *testing.T) {
	store, err := NewSpotStore(filepath.Join("..", "data", "spots.json"))
	if err != nil {
		t.Fatal(err)
	}
	index, err := NewSEOIndex("https://openswells.com", store)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(index.robots, []byte("Disallow: /api/")) ||
		!bytes.Contains(index.robots, []byte("Sitemap: https://openswells.com/sitemap.xml")) {
		t.Fatalf("unexpected robots.txt:\n%s", index.robots)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/robots.txt", index.handleRobots)
	router.GET("/sitemap.xml", index.handleSitemapIndex)
	router.GET("/sitemaps/:name", index.handleSitemap)
	for _, path := range []string{"/robots.txt", "/sitemap.xml", "/sitemaps/pages.xml", "/sitemaps/spots-1.xml"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Errorf("%s status = %d", path, response.Code)
		}
	}

	var sitemapRoot sitemapIndexDocument
	if err := xml.Unmarshal(index.xmlDocs["sitemap.xml"], &sitemapRoot); err != nil {
		t.Fatal(err)
	}
	if len(sitemapRoot.Sitemaps) != 4 {
		t.Errorf("sitemap index has %d children, want 4", len(sitemapRoot.Sitemaps))
	}

	allLocations := make(map[string]bool)
	spotCount := 0
	for name, document := range index.xmlDocs {
		var root struct{ XMLName xml.Name }
		if err := xml.Unmarshal(document, &root); err != nil {
			t.Fatalf("%s is invalid XML: %v", name, err)
		}
		if !strings.HasPrefix(string(document), xml.Header) {
			t.Errorf("%s is missing XML declaration", name)
		}
		if !strings.HasPrefix(name, "spots-") {
			continue
		}
		var set sitemapURLSet
		if err := xml.Unmarshal(document, &set); err != nil {
			t.Fatal(err)
		}
		if len(set.URLs) > spotSitemapSize {
			t.Errorf("%s has %d URLs", name, len(set.URLs))
		}
		spotCount += len(set.URLs)
		for _, item := range set.URLs {
			if allLocations[item.Loc] {
				t.Errorf("duplicate sitemap URL %s", item.Loc)
			}
			allLocations[item.Loc] = true
			if strings.Contains(item.Loc, "/spot/") || strings.Contains(item.Loc, "?") {
				t.Errorf("non-canonical sitemap URL %s", item.Loc)
			}
		}
	}
	if spotCount != store.Count() {
		t.Errorf("sitemaps contain %d spots, want %d", spotCount, store.Count())
	}
	if len(index.xmlDocs) != 5 { // root, pages, and three spot documents
		t.Errorf("generated %d sitemap documents, want 5", len(index.xmlDocs))
	}
}

func TestSurfSpotCanonicalRouteAndRenderedSEO(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tempDir := t.TempDir()
	spotsPath := filepath.Join(tempDir, "spots.json")
	spotsJSON := `[
		{"id":"test-break","name":"Test Break","region":"California","lat":33.1,"lon":-118.2},
		{"id":"nearby-break","name":"Nearby Break","region":"California","lat":33.2,"lon":-118.2}
	]`
	if err := os.WriteFile(spotsPath, []byte(spotsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewSpotStore(spotsPath)
	if err != nil {
		t.Fatal(err)
	}
	previousStore := spotStore
	spotStore = store
	t.Cleanup(func() { spotStore = previousStore })

	tmpl := loadTemplates(filepath.Join("..", "web", "templates"))
	router := gin.New()
	router.GET("/surf-spots/:slug", spotPageHandler(tmpl, tempDir, nil, "https://openswells.com"))

	request := httptest.NewRequest(http.MethodGet, "/surf-spots/test-break?utm_source=test", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("valid spot status = %d: %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{
		"<title>Test Break Surf Forecast | OpenSwells</title>",
		`<link rel="canonical" href="https://openswells.com/surf-spots/test-break">`,
		`<meta property="og:type" content="website">`,
		`<script type="application/ld+json">`,
		`<h1 class="station-title">Test Break Surf Forecast</h1>`,
		"Latitude 33.100000, longitude -118.200000",
		`href="/map?spot=test-break"`,
		`<a href="/map?spot=test-break">Map</a>`,
		`html.embedded .spot-breadcrumbs { display: none; }`,
		`href="/surf-spots/nearby-break"`,
	} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Errorf("spot page is missing %q", expected)
		}
	}
	if strings.Count(response.Body.String(), "<h1") != 1 {
		t.Errorf("spot page has %d H1 elements", strings.Count(response.Body.String(), "<h1"))
	}
	if strings.Contains(response.Body.String(), "utm_source") {
		t.Error("tracking parameter leaked into rendered metadata")
	}

	for _, path := range []string{"/surf-spots/missing", "/spot/test-break"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, response.Code)
		}
	}
}
