package main

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

const spotSitemapSize = 2000

type SEOPage struct {
	Title          string
	Description    string
	Canonical      string
	OpenGraphTitle string
	StructuredData interface{}
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
				"itemListElement": []interface{}{
					map[string]interface{}{"@type": "ListItem", "position": 1, "name": "Map", "item": siteURL + "/map?spot=" + url.QueryEscape(spot.ID)},
					map[string]interface{}{"@type": "ListItem", "position": 2, "name": name, "item": canonical},
				},
			},
		},
	}
	return SEOPage{
		Title: heading + " | OpenSwells", Description: description, Canonical: canonical,
		OpenGraphTitle: heading, StructuredData: structuredData,
	}
}

type sitemapLocation struct {
	Loc string `xml:"loc"`
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

type SEOIndex struct {
	robots  []byte
	xmlDocs map[string][]byte
}

func xmlDocument(v interface{}) ([]byte, error) {
	body, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}

func NewSEOIndex(siteURL string, store *SpotStore) (*SEOIndex, error) {
	if store == nil {
		return nil, fmt.Errorf("spot store is required")
	}
	index := &SEOIndex{xmlDocs: make(map[string][]byte)}
	index.robots = []byte("User-agent: *\nAllow: /\nDisallow: /api/\nSitemap: " + siteURL + "/sitemap.xml\n")

	pages, err := xmlDocument(sitemapURLSet{
		XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9",
		URLs:  []sitemapLocation{{Loc: siteURL + "/"}, {Loc: siteURL + "/map"}},
	})
	if err != nil {
		return nil, err
	}
	index.xmlDocs["pages.xml"] = pages

	children := []sitemapLocation{{Loc: siteURL + "/sitemaps/pages.xml"}}
	spots := store.All()
	for start, fileNumber := 0, 1; start < len(spots); start, fileNumber = start+spotSitemapSize, fileNumber+1 {
		end := start + spotSitemapSize
		if end > len(spots) {
			end = len(spots)
		}
		locations := make([]sitemapLocation, 0, end-start)
		for _, spot := range spots[start:end] {
			locations = append(locations, sitemapLocation{Loc: siteURL + "/surf-spots/" + url.PathEscape(spot.ID)})
		}
		name := fmt.Sprintf("spots-%d.xml", fileNumber)
		doc, err := xmlDocument(sitemapURLSet{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9", URLs: locations})
		if err != nil {
			return nil, err
		}
		index.xmlDocs[name] = doc
		children = append(children, sitemapLocation{Loc: siteURL + "/sitemaps/" + name})
	}

	root, err := xmlDocument(sitemapIndexDocument{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9", Sitemaps: children})
	if err != nil {
		return nil, err
	}
	index.xmlDocs["sitemap.xml"] = root
	return index, nil
}

func (s *SEOIndex) handleRobots(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=3600")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", s.robots)
}

func (s *SEOIndex) handleSitemapIndex(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=3600")
	c.Data(http.StatusOK, "application/xml; charset=utf-8", s.xmlDocs["sitemap.xml"])
}

func (s *SEOIndex) handleSitemap(c *gin.Context) {
	doc, ok := s.xmlDocs[c.Param("name")]
	if !ok || c.Param("name") == "sitemap.xml" {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "public, max-age=3600")
	c.Data(http.StatusOK, "application/xml; charset=utf-8", doc)
}
