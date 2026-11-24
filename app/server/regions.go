package main

// Region hub pages: /surf-forecasts lists countries, and every level of a
// spot's region ("USA, California, San Diego County") gets a page at
// /surf-forecasts/usa/california/san-diego-county listing its sub-regions
// and spots with the conditions right now. They give search engines (and
// people) a route from "California surf forecast" down to a single break.

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"unicode"
)

// RegionNode is one level of the region hierarchy.
type RegionNode struct {
	Name     string   // this level, e.g. "San Diego County"
	Path     []string // every level from the country down
	Slug     string   // "usa/california/san-diego-county"; "" for the root
	Parent   *RegionNode
	Children []*RegionNode // by name
	Spots    []Spot        // spots whose region ends at this level, by name
	// SpotCount includes every descendant's spots.
	SpotCount int
	// Bounding box of all its spots, for the share card's map.
	MinLat, MaxLat, MinLon, MaxLon float64
}

type RegionTree struct {
	Root   *RegionNode
	bySlug map[string]*RegionNode
	bySpot map[string]*RegionNode
}

var regionTree *RegionTree

// regionSlug lowercases s and joins its letters and digits with hyphens.
func regionSlug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
			continue
		}
		dash = true
	}
	return b.String()
}

// regionPath splits a spot's region into its levels, country first
// ("USA, California, San Diego County"). spots.json is kept in that form;
// TestSpotRegionsAreClean checks it.
func regionPath(region string) []string {
	var path []string
	for _, part := range strings.Split(region, ",") {
		if part = strings.TrimSpace(part); part != "" {
			path = append(path, part)
		}
	}
	return path
}

func NewRegionTree(spots []Spot) *RegionTree {
	t := &RegionTree{
		Root:   &RegionNode{MinLat: 90, MaxLat: -90, MinLon: 180, MaxLon: -180},
		bySlug: map[string]*RegionNode{},
		bySpot: map[string]*RegionNode{},
	}
	children := map[*RegionNode]map[string]*RegionNode{}
	for _, spot := range spots {
		path := regionPath(spot.Region)
		if len(path) == 0 {
			continue
		}
		node := t.Root
		node.include(spot)
		for _, name := range path {
			slug := regionSlug(name)
			if slug == "" {
				break
			}
			if children[node] == nil {
				children[node] = map[string]*RegionNode{}
			}
			child := children[node][slug]
			if child == nil {
				child = &RegionNode{
					Name: name, Parent: node,
					Path:   append(append([]string(nil), node.Path...), name),
					Slug:   strings.TrimPrefix(node.Slug+"/"+slug, "/"),
					MinLat: 90, MaxLat: -90, MinLon: 180, MaxLon: -180,
				}
				children[node][slug] = child
				node.Children = append(node.Children, child)
				t.bySlug[child.Slug] = child
			}
			node = child
			node.include(spot)
		}
		node.Spots = append(node.Spots, spot)
		t.bySpot[spot.ID] = node
	}
	var sortNode func(n *RegionNode)
	sortNode = func(n *RegionNode) {
		sort.Slice(n.Children, func(i, j int) bool { return n.Children[i].Name < n.Children[j].Name })
		sort.Slice(n.Spots, func(i, j int) bool {
			if n.Spots[i].Name != n.Spots[j].Name {
				return n.Spots[i].Name < n.Spots[j].Name
			}
			return n.Spots[i].ID < n.Spots[j].ID
		})
		for _, c := range n.Children {
			sortNode(c)
		}
	}
	sortNode(t.Root)
	return t
}

func (n *RegionNode) include(spot Spot) {
	n.SpotCount++
	n.MinLat, n.MaxLat = math.Min(n.MinLat, spot.Lat), math.Max(n.MaxLat, spot.Lat)
	n.MinLon, n.MaxLon = math.Min(n.MinLon, spot.Lon), math.Max(n.MaxLon, spot.Lon)
}

func (t *RegionTree) Get(slug string) (*RegionNode, bool) {
	if t == nil {
		return nil, false
	}
	n, ok := t.bySlug[strings.Trim(slug, "/")]
	return n, ok
}

// ForSpot is the deepest region a spot belongs to.
func (t *RegionTree) ForSpot(id string) (*RegionNode, bool) {
	if t == nil {
		return nil, false
	}
	n, ok := t.bySpot[id]
	return n, ok
}

// All lists every region node, parents before children.
func (t *RegionTree) All() []*RegionNode {
	if t == nil {
		return nil
	}
	var out []*RegionNode
	var walk func(n *RegionNode)
	walk = func(n *RegionNode) {
		for _, c := range n.Children {
			out = append(out, c)
			walk(c)
		}
	}
	walk(t.Root)
	return out
}

// URL is the region page's path.
func (n *RegionNode) URL() string {
	if n == nil || n.Slug == "" {
		return regionIndexPath
	}
	parts := strings.Split(n.Slug, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return regionIndexPath + "/" + strings.Join(parts, "/")
}

const regionIndexPath = "/surf-forecasts"

// DisplayName names the region for titles: its level, with its parent's
// for context below country level ("San Diego County, California").
func (n *RegionNode) DisplayName() string {
	if len(n.Path) >= 2 {
		return n.Path[len(n.Path)-1] + ", " + n.Path[len(n.Path)-2]
	}
	return n.Name
}

// FullName is every level, most specific first ("San Diego County,
// California, USA").
func (n *RegionNode) FullName() string {
	parts := make([]string, len(n.Path))
	for i, p := range n.Path {
		parts[len(n.Path)-1-i] = p
	}
	return strings.Join(parts, ", ")
}

// articleNames take "the" in a sentence ("surf spots in the USA").
var articleNames = map[string]bool{
	"USA": true, "UK": true, "UAE": true, "Netherlands": true, "Philippines": true,
	"Bahamas": true, "Maldives": true, "Seychelles": true, "Dominican Republic": true,
	"Azores": true, "Marshall Islands": true, "Mariana Islands": true, "Virgin Islands": true,
}

// ProseName is FullName as it reads mid-sentence: "the USA", but
// "California, USA".
func (n *RegionNode) ProseName() string {
	if len(n.Path) == 1 && articleNames[n.Name] {
		return "the " + n.Name
	}
	return n.FullName()
}

// Ancestors are the region's parents from the country down, excluding
// the root and the region itself.
func (n *RegionNode) Ancestors() []*RegionNode {
	var out []*RegionNode
	for p := n.Parent; p != nil && p.Slug != ""; p = p.Parent {
		out = append([]*RegionNode{p}, out...)
	}
	return out
}

// Descendants are the spots in this region and all below it.
func (n *RegionNode) Descendants() []Spot {
	out := append([]Spot(nil), n.Spots...)
	for _, c := range n.Children {
		out = append(out, c.Descendants()...)
	}
	return out
}

// cardView frames the region's spots for its share card, to the right of
// the panel.
func (n *RegionNode) cardView() mercView {
	return n.mapView(cardW, cardH, cardPanelW)
}

// mapView frames the region's spots in a w x h image whose map shows from
// pixel column left (the rest sits under a panel), with room around them.
func (n *RegionNode) mapView(w, h, left int) mercView {
	minLon, maxLon := lonInterval(n.Descendants())
	mapW := float64(w - left)
	// Degrees per pixel to fit the box with padding, no closer in than a
	// coastline needs for context, and no wider than the world.
	lonSpan := (maxLon - minLon) * 1.4
	latSpan := (mercY(n.MaxLat) - mercY(n.MinLat)) * 1.4
	perPx := math.Max(math.Max(lonSpan/mapW, latSpan/float64(h)), 3.0/mapW)
	perPx = math.Min(perPx, 360.0/mapW)
	lon := (minLon + maxLon) / 2
	lat := invMercY((mercY(n.MaxLat) + mercY(n.MinLat)) / 2)
	fx := (float64(left) + mapW/2) / float64(w)
	return viewAt(lon, lat, perPx*float64(w), w, h, fx, 0.5)
}

// lonInterval is the shortest longitude range covering every spot, going
// across the antimeridian when that is shorter (Fiji). maxLon may exceed
// 180.
func lonInterval(spots []Spot) (minLon, maxLon float64) {
	if len(spots) == 0 {
		return -180, 180
	}
	lons := make([]float64, len(spots))
	for i, s := range spots {
		lons[i] = math.Mod(s.Lon+540, 360) - 180
	}
	sort.Float64s(lons)
	// The interval is the circle minus its widest gap between spots.
	gap, after := lons[0]+360-lons[len(lons)-1], 0
	for i := 1; i < len(lons); i++ {
		if g := lons[i] - lons[i-1]; g > gap {
			gap, after = g, i
		}
	}
	if after == 0 {
		return lons[0], lons[len(lons)-1]
	}
	return lons[after], lons[after-1] + 360
}

// regionSEOName is the region for a title, made unique when two regions
// share a display name.
func (t *RegionTree) checkUniqueNames() error {
	seen := map[string]string{}
	for _, n := range t.All() {
		name := n.DisplayName()
		if prev, ok := seen[name]; ok {
			return fmt.Errorf("regions %q and %q share the name %q", prev, n.Slug, name)
		}
		seen[name] = n.Slug
	}
	return nil
}
