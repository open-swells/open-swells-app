package main

import (
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// regionListAllMax is the most spots a region page lists in full; bigger
// regions (countries, large states) list their sub-regions and the best
// spots right now.
const regionListAllMax = 150

// regionIndexMin is the fewest spots a region needs for search engines to
// index its page; smaller ones still render (they are breadcrumbs) but say
// little the spot page doesn't.
const regionIndexMin = 3

type Crumb struct {
	Name, URL string
}

type RegionSpotRow struct {
	ID, Name, URL string
	Area          string // its sub-region below the page's region, if any
	Snap          SpotSnapshot
	HasSnap       bool
}

type RegionChildRow struct {
	Name, URL string
	SpotCount int
	Best      RegionSpotRow
	HasBest   bool
}

type RegionPageData struct {
	SEO         SEOPage
	Heading     string
	Crumbs      []Crumb
	Intro       []string
	Children    []RegionChildRow
	Spots       []RegionSpotRow // every spot, or the best ones now
	Direct      []RegionSpotRow // large regions: spots filed at this level, beyond Spots
	ListsAll    bool
	SpotCount   int
	MapURL      string
	MapImage    string // the region's map image, with its spots
	IsIndex     bool
	Countries   []RegionChildRow // index page only
	SnapshotsOK bool
}

func regionSpotRow(n *RegionNode, s Spot, snaps SnapshotSet) RegionSpotRow {
	row := RegionSpotRow{ID: s.ID, Name: s.Name, URL: "/surf-spots/" + s.ID}
	row.Snap, row.HasSnap = snaps.Get(s.ID)
	if path := regionPath(s.Region); len(path) > len(n.Path) {
		row.Area = strings.Join(path[len(n.Path):], ", ")
	}
	return row
}

func regionCrumbs(n *RegionNode) []Crumb {
	crumbs := []Crumb{{Name: "Surf forecasts", URL: regionIndexPath}}
	for _, a := range n.Ancestors() {
		crumbs = append(crumbs, Crumb{Name: a.Name, URL: a.URL()})
	}
	return crumbs
}

// regionIntro writes the page's opening paragraphs from its spots and
// their snapshots.
func regionIntro(n *RegionNode, spots []Spot, snaps SnapshotSet) []string {
	var paras []string
	first := fmt.Sprintf("Free 16-day surf forecasts for %s surf %s in %s",
		formatCount(len(spots)), plural(len(spots), "spot", "spots"), n.ProseName())
	if len(n.Children) > 0 {
		var names []string
		for i, c := range n.Children {
			if i == 4 {
				break
			}
			names = append(names, c.Name)
		}
		if len(n.Children) > 4 {
			names = append(names, "more")
		}
		first += ", across " + joinList(names)
	}
	first += ". Each forecast has the breaking surf height, swell, wind and tide by the hour, rated for how well the spot handles them."
	paras = append(paras, first)

	if best, ok := bestSnapshot(spots, snaps); ok {
		working := 0
		rated := 0
		for _, s := range spots {
			if snap, ok := snaps.Get(s.ID); ok && snap.HasNow() {
				rated++
				if snap.Rating != "poor" {
					working++
				}
			}
		}
		now := fmt.Sprintf("Right now the best-rated spot is %s, with %s surf rated %s",
			best.Spot.Name, surfFeet(best.Snap.Surf), best.Snap.Rating)
		if swell := best.Snap.swellPhrase(); swell != "" {
			now += " on a " + swell + " swell"
		}
		now += "."
		if rated > 1 {
			now += fmt.Sprintf(" %d of %d spots rate fair or better.", working, rated)
		}
		paras = append(paras, now)
	}
	if p := regionWeek(spots, snaps); p != "" {
		paras = append(paras, p)
	}
	return paras
}

// regionWeek names the day this week with the most spots rated good or
// better (fair when none are).
func regionWeek(spots []Spot, snaps SnapshotSet) string {
	good := map[string]int{}
	fair := map[string]int{}
	for _, s := range spots {
		snap, ok := snaps.Get(s.ID)
		if !ok {
			continue
		}
		for _, d := range snap.Days {
			switch d.Condition {
			case "good", "epic":
				good[d.Date]++
				fair[d.Date]++
			case "fair":
				fair[d.Date]++
			}
		}
	}
	pick := func(counts map[string]int) (string, int) {
		day, most := "", 0
		for d, c := range counts {
			if c > most || (c == most && d < day) {
				day, most = d, c
			}
		}
		return day, most
	}
	if day, count := pick(good); count > 0 {
		return fmt.Sprintf("Looking at the week ahead, %s has the most spots rated good or better (%d).",
			dayName(day, "Monday, January 2"), count)
	}
	if day, count := pick(fair); count > 0 {
		return fmt.Sprintf("No spot here rates good in the week ahead; %s has the most rated fair (%d).",
			dayName(day, "Monday, January 2"), count)
	}
	return ""
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func regionPageData(staticDir, siteURL string, n *RegionNode) RegionPageData {
	spots := n.Descendants()
	snaps := spotSnapshots.Current()
	heading := n.DisplayName() + " Surf Forecast"
	description := fmt.Sprintf("Free 16-day surf forecasts and surf reports for %s %s in %s: surf height, swell, wind and tide by the hour.",
		formatCount(len(spots)), plural(len(spots), "spot", "spots"), n.ProseName())
	data := RegionPageData{
		Heading:     heading,
		Crumbs:      regionCrumbs(n),
		Intro:       regionIntro(n, spots, snaps),
		SpotCount:   len(spots),
		ListsAll:    len(spots) <= regionListAllMax,
		SnapshotsOK: snaps != nil,
	}
	if len(spots) > 0 {
		data.MapURL = "/map?spot=" + spots[0].ID
		data.MapImage = regionMapPath(staticDir, n)
	}
	for _, c := range n.Children {
		row := RegionChildRow{Name: c.Name, URL: c.URL(), SpotCount: c.SpotCount}
		if best, ok := bestSnapshot(c.Descendants(), snaps); ok {
			row.Best, row.HasBest = regionSpotRow(n, best.Spot, snaps), true
		}
		data.Children = append(data.Children, row)
	}
	if data.ListsAll {
		for _, s := range spots {
			data.Spots = append(data.Spots, regionSpotRow(n, s, snaps))
		}
		sort.SliceStable(data.Spots, func(i, j int) bool { return data.Spots[i].Name < data.Spots[j].Name })
	} else {
		// The best dozen right now, then the spots filed at this level
		// itself, which no sub-region page lists.
		var rows []RegionSpotRow
		for _, s := range spots {
			if row := regionSpotRow(n, s, snaps); row.HasSnap && row.Snap.HasNow() {
				rows = append(rows, row)
			}
		}
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].Snap.Score != rows[j].Snap.Score {
				return rows[i].Snap.Score > rows[j].Snap.Score
			}
			return rows[i].Snap.SurfHiFt > rows[j].Snap.SurfHiFt
		})
		if len(rows) > 12 {
			rows = rows[:12]
		}
		data.Spots = rows
		listed := map[string]bool{}
		for _, r := range rows {
			listed[r.ID] = true
		}
		for _, s := range n.Spots {
			if !listed[s.ID] {
				data.Direct = append(data.Direct, regionSpotRow(n, s, snaps))
			}
		}
	}

	canonical := siteURL + n.URL()
	crumbs := []interface{}{}
	for i, c := range append(data.Crumbs, Crumb{Name: n.Name, URL: n.URL()}) {
		crumbs = append(crumbs, map[string]interface{}{"@type": "ListItem", "position": i + 1, "name": c.Name, "item": siteURL + c.URL})
	}
	// The list in the structured data is the list on the page.
	items := []interface{}{}
	for _, r := range append(append([]RegionSpotRow(nil), data.Spots...), data.Direct...) {
		items = append(items, map[string]interface{}{"@type": "ListItem", "position": len(items) + 1, "url": siteURL + r.URL, "name": r.Name})
	}
	webPage := map[string]interface{}{
		"@type": "CollectionPage", "@id": canonical + "#webpage", "url": canonical,
		"name": heading, "description": description,
		"breadcrumb": map[string]interface{}{"@id": canonical + "#breadcrumb"},
	}
	if len(items) > 0 {
		webPage["mainEntity"] = map[string]interface{}{"@type": "ItemList", "numberOfItems": len(items), "itemListElement": items}
	}
	data.SEO = SEOPage{
		Title:          fmt.Sprintf("%s Surf Forecast & Surf Report | OpenSwells", n.DisplayName()),
		Description:    description,
		Canonical:      canonical,
		OpenGraphTitle: heading,
		ImageURL:       siteURL + regionCardPath(staticDir, n),
		ImageAlt:       fmt.Sprintf("Swell height map of %s with its surf spots", n.ProseName()),
		NoIndex:        len(spots) < regionIndexMin,
		StructuredData: map[string]interface{}{
			"@context": "https://schema.org",
			"@graph": []interface{}{
				webPage,
				map[string]interface{}{"@type": "BreadcrumbList", "@id": canonical + "#breadcrumb", "itemListElement": crumbs},
			},
		},
	}
	if len(data.Intro) > 1 {
		data.SEO.OGDescription = data.Intro[1] + " " + description
	}
	return data
}

func regionIndexData(staticDir, siteURL string, tree *RegionTree) RegionPageData {
	data := RegionPageData{
		IsIndex:  true,
		MapImage: regionMapPath(staticDir, tree.Root),
		Heading:  "Surf Forecasts by Country",
		Crumbs:   []Crumb{{Name: "Map", URL: "/map"}},
	}
	total := 0
	for _, c := range tree.Root.Children {
		data.Countries = append(data.Countries, RegionChildRow{Name: c.Name, URL: c.URL(), SpotCount: c.SpotCount})
		total += c.SpotCount
	}
	data.SpotCount = total
	data.Intro = []string{fmt.Sprintf("Free 16-day surf forecasts for %s surf spots in %d countries and territories. Pick a country to see its regions and spots, with the surf rated for right now.",
		formatCount(total), len(tree.Root.Children))}
	canonical := siteURL + regionIndexPath
	data.SEO = SEOPage{
		Title:          "Surf Forecasts by Country | OpenSwells",
		Description:    data.Intro[0],
		Canonical:      canonical,
		OpenGraphTitle: data.Heading,
		StructuredData: map[string]interface{}{
			"@context": "https://schema.org", "@type": "CollectionPage", "url": canonical,
			"name": data.Heading, "description": data.Intro[0],
		},
	}
	return data
}

func regionHandler(tmpl *template.Template, staticDir, siteURL string, tree *RegionTree) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := strings.Trim(c.Param("path"), "/")
		if path == "" {
			renderTemplate(c, tmpl, "region.html", regionIndexData(staticDir, siteURL, tree))
			return
		}
		if path == "map.jpg" || strings.HasSuffix(path, "/map.jpg") {
			n := tree.Root
			if slug := strings.TrimSuffix(strings.TrimSuffix(path, "map.jpg"), "/"); slug != "" {
				var ok bool
				if n, ok = tree.Get(slug); !ok {
					c.Status(http.StatusNotFound)
					return
				}
			}
			serveFrameImage(c, staticDir, mapCache, "map\x00"+n.Slug, "image/jpeg", 3600, func(frame *heatmapFrame) ([]byte, error) {
				return regionMap(frame, n)
			})
			return
		}
		if strings.HasSuffix(path, "/card.jpg") || path == "card.jpg" {
			n, ok := tree.Get(strings.TrimSuffix(strings.TrimSuffix(path, "card.jpg"), "/"))
			if !ok {
				c.Status(http.StatusNotFound)
				return
			}
			serveFrameImage(c, staticDir, cardCache, "region\x00"+n.Slug, "image/jpeg", 3600, func(frame *heatmapFrame) ([]byte, error) {
				return regionCard(frame, n)
			})
			return
		}
		lower := strings.ToLower(path)
		n, ok := tree.Get(lower)
		if !ok {
			c.String(http.StatusNotFound, "Unknown region")
			return
		}
		if lower != path {
			c.Redirect(http.StatusMovedPermanently, n.URL())
			return
		}
		renderTemplate(c, tmpl, "region.html", regionPageData(staticDir, siteURL, n))
	}
}
