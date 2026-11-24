package main

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const spotSitemapSize = 2000

type SEOPage struct {
	Title          string
	Description    string
	Canonical      string
	OpenGraphTitle string
	// OGDescription replaces Description in link previews, which can say
	// what the surf is doing now; search snippets keep Description.
	OGDescription string
	// ImageURL is the absolute Open Graph image (a 1200x630 share card).
	ImageURL       string
	ImageAlt       string
	NoIndex        bool
	StructuredData interface{}
}

// PreviewDescription is the description for link previews.
func (p SEOPage) PreviewDescription() string {
	if p.OGDescription != "" {
		return p.OGDescription
	}
	return p.Description
}

func canonicalSiteURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" {
		return "", fmt.Errorf("SITE_URL must be an http(s) origin without a path: %q", raw)
	}
	return raw, nil
}

func spotSEOPage(siteURL string, store *SpotStore, spot Spot) SEOPage {
	name := store.SEOName(spot)
	canonical := siteURL + "/surf-spots/" + url.PathEscape(spot.ID)
	region := strings.TrimSpace(spot.Region)
	where := ""
	if region != "" {
		if name == spot.Name {
			where = " in " + region
		} else {
			where = ", located in " + region
		}
	}
	description := fmt.Sprintf("Current and upcoming surf conditions for %s%s, including swell height, period, direction, wind, and tide information when available.", name, where)
	heading := name + " Surf Forecast"
	placeID := canonical + "#place"
	// Breadcrumbs follow the region hub pages down to the spot, or
	// fall back to the map for a spot without a region.
	crumbs := []interface{}{}
	addCrumb := func(name, item string) {
		crumbs = append(crumbs, map[string]interface{}{"@type": "ListItem", "position": len(crumbs) + 1, "name": name, "item": item})
	}
	if node, ok := regionTree.ForSpot(spot.ID); ok {
		addCrumb("Surf forecasts", siteURL+regionIndexPath)
		for _, a := range append(node.Ancestors(), node) {
			addCrumb(a.Name, siteURL+a.URL())
		}
	} else {
		addCrumb("Map", siteURL+"/map?spot="+url.QueryEscape(spot.ID))
	}
	addCrumb(name, canonical)
	structuredData := map[string]interface{}{
		"@context": "https://schema.org",
		"@graph": []interface{}{
			map[string]interface{}{
				"@type": "WebPage", "@id": canonical + "#webpage", "url": canonical,
				"name": heading, "description": description,
				"mainEntity": map[string]interface{}{"@id": placeID},
				"breadcrumb": map[string]interface{}{"@id": canonical + "#breadcrumb"},
			},
			map[string]interface{}{
				"@type": "Place", "@id": placeID, "name": name,
				"geo": map[string]interface{}{
					"@type": "GeoCoordinates", "latitude": spot.Lat, "longitude": spot.Lon,
				},
			},
			map[string]interface{}{
				"@type": "BreadcrumbList", "@id": canonical + "#breadcrumb",
				"itemListElement": crumbs,
			},
		},
	}
	return SEOPage{
		Title: heading + " | OpenSwells", Description: description, Canonical: canonical,
		OpenGraphTitle: heading, StructuredData: structuredData,
	}
}

type sitemapLocation struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

type sitemapURLSet struct {
	XMLName xml.Name          `xml:"urlset"`
	XMLNS   string            `xml:"xmlns,attr"`
	URLs    []sitemapLocation `xml:"url"`
}

type sitemapIndexDocument struct {
	XMLName  xml.Name          `xml:"sitemapindex"`
	XMLNS    string            `xml:"xmlns,attr"`
	Sitemaps []sitemapLocation `xml:"sitemap"`
}

const sitemapXMLNS = "http://www.sitemaps.org/schemas/sitemap/0.9"

// SEOIndex serves robots.txt and the sitemaps. Every listed page shows the
// current forecast, so each URL's lastmod is when the current run was
// published; the documents are rebuilt when a new run lands.
type SEOIndex struct {
	robots    []byte
	siteURL   string
	staticDir string
	names     []string            // child sitemaps, in index order
	locs      map[string][]string // child sitemap -> page URLs

	mu       sync.Mutex
	builtFor string // the lastmod the documents were built with
	docs     map[string][]byte
}

func xmlDocument(v interface{}) ([]byte, error) {
	body, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}

func NewSEOIndex(siteURL string, store *SpotStore, regions *RegionTree, staticDir string) (*SEOIndex, error) {
	if store == nil {
		return nil, fmt.Errorf("spot store is required")
	}
	index := &SEOIndex{siteURL: siteURL, staticDir: staticDir, locs: map[string][]string{}}
	index.robots = []byte("User-agent: OAI-SearchBot\nAllow: /\nDisallow: /api/\n\nUser-agent: *\nAllow: /\nDisallow: /api/\nSitemap: " + siteURL + "/sitemap.xml\n")

	add := func(name string, locs []string) {
		index.names = append(index.names, name)
		index.locs[name] = locs
	}
	add("pages.xml", []string{siteURL + "/", siteURL + "/map", siteURL + regionIndexPath})
	var regionLocs []string
	for _, n := range regions.All() {
		if n.SpotCount >= regionIndexMin {
			regionLocs = append(regionLocs, siteURL+n.URL())
		}
	}
	if len(regionLocs) > 0 {
		add("regions.xml", regionLocs)
	}
	spots := store.All()
	for start, fileNumber := 0, 1; start < len(spots); start, fileNumber = start+spotSitemapSize, fileNumber+1 {
		end := min(start+spotSitemapSize, len(spots))
		locs := make([]string, 0, end-start)
		for _, spot := range spots[start:end] {
			locs = append(locs, siteURL+"/surf-spots/"+url.PathEscape(spot.ID))
		}
		add(fmt.Sprintf("spots-%d.xml", fileNumber), locs)
	}
	if _, err := index.documents(); err != nil {
		return nil, err
	}
	return index, nil
}

// lastMod is the current run's publish time, "" without a run.
func (s *SEOIndex) lastMod() string {
	if s.staticDir == "" {
		return ""
	}
	if t, ok := currentHeatmapMeta(s.staticDir).Published(); ok {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

// documents are the sitemap documents for the current run.
func (s *SEOIndex) documents() (map[string][]byte, error) {
	lastMod := s.lastMod()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.docs != nil && s.builtFor == lastMod {
		return s.docs, nil
	}
	docs := make(map[string][]byte, len(s.names)+1)
	children := make([]sitemapLocation, 0, len(s.names))
	for _, name := range s.names {
		urls := make([]sitemapLocation, 0, len(s.locs[name]))
		for _, loc := range s.locs[name] {
			urls = append(urls, sitemapLocation{Loc: loc, LastMod: lastMod})
		}
		doc, err := xmlDocument(sitemapURLSet{XMLNS: sitemapXMLNS, URLs: urls})
		if err != nil {
			return nil, err
		}
		docs[name] = doc
		children = append(children, sitemapLocation{Loc: s.siteURL + "/sitemaps/" + name, LastMod: lastMod})
	}
	root, err := xmlDocument(sitemapIndexDocument{XMLNS: sitemapXMLNS, Sitemaps: children})
	if err != nil {
		return nil, err
	}
	docs["sitemap.xml"] = root
	s.docs, s.builtFor = docs, lastMod
	return docs, nil
}

func (s *SEOIndex) handleRobots(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=3600")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", s.robots)
}

func (s *SEOIndex) serve(c *gin.Context, name string) {
	docs, err := s.documents()
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	doc, ok := docs[name]
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "public, max-age=3600")
	c.Data(http.StatusOK, "application/xml; charset=utf-8", doc)
}

func (s *SEOIndex) handleSitemapIndex(c *gin.Context) {
	s.serve(c, "sitemap.xml")
}

func (s *SEOIndex) handleSitemap(c *gin.Context) {
	if c.Param("name") == "sitemap.xml" {
		c.Status(http.StatusNotFound)
		return
	}
	s.serve(c, c.Param("name"))
}
