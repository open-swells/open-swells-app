package main

import (
    "context"
    "io"
    "strings"
    "sync"
    "time"
    "bytes"
	"html/template"
    "fmt"
	"net/http"
    "strconv"
    "sort"
    "database/sql"
    "log"

    _ "github.com/mattn/go-sqlite3"
	"github.com/gin-gonic/gin"
    "github.com/go-resty/resty/v2"
    "firebase.google.com/go/v4"
    "firebase.google.com/go/v4/auth"
    "google.golang.org/api/option"
)


type ForecastRow struct {
    Date string
    PrimaryWaveHeight string
    PrimaryPeriod string
    PrimaryDegrees string
    SecondaryWaveHeight string
    SecondaryPeriod string
    SecondaryDegrees string
    TertiaryWaveHeight string
    TertiaryPeriod string
    TertiaryDegrees string
    QuaternaryWaveHeight string
    QuaternaryPeriod string
    QuaternaryDegrees string
}

type ForecastData struct {
    Forecast    []ForecastRow
    Date        string
}

type BuoyLocation struct {
    StationID string
    Name      string
    Latitude  float64
    Longitude float64
}

type Buoy struct {
    ID       string
    Name     string
    Forecast map[string]interface{}
}

// Global map of all buoy locations
var BuoyLocations = map[string]BuoyLocation{
    "41002": {"41002", "SOUTH HATTERAS - 225 NM South of Cape Hatteras", 31.759, -74.936},
    "41004": {"41004", "EDISTO - 41 NM Southeast of Charleston, SC", 32.502, -79.099},
    "41008": {"41008", "GRAYS REEF - 40 NM Southeast of Savannah, GA", 31.4, -80.866},
    "41009": {"41009", "CANAVERAL 20 NM East of Cape Canaveral, FL", 28.508, -80.185},
    "41013": {"41013", "Frying Pan Shoals, NC", 33.441, -77.764},
    "41025": {"41025", "Diamond Shoals, NC", 35.01, -75.454},
    "41040": {"41040", "NORTH EQUATORIAL ONE- 470 NM East of Martinique", 14.541, -53.137},
    "41043": {"41043", "NE PUERTO RICO - 170 NM NNE of San Juan, PR", 21.026, -64.793},
    "41044": {"41044", "NE ST MARTIN - 330 NM NE St Martin Is", 21.582, -58.63},
    "41046": {"41046", "EAST BAHAMAS - 335 NM East of San Salvador Is,  Bahamas", 23.822, -68.393},
    "41047": {"41047", "NE BAHAMAS - 350 NM ENE of Nassau, Bahamas", 27.465, -71.452},
    "41049": {"41049", "SOUTH BERMUDA - 300 NM SSE of Bermuda", 27.545, -63.012},
    "41052": {"41052", "South of St. John, VI", 18.249, -64.763},
    "41053": {"41053", "San Juan, PR", 18.474, -66.099},
    "41056": {"41056", "Vieques Island, PR", 18.261, -65.464},
    "41065": {"41065", "Capers Nearshore Waves (CAP2WAVE)", 32.802, -79.619},
    "41067": {"41067", "FRP2WAVE", 32.276, -80.406},
    "41070": {"41070", "Ponce de Leon Inlet Waves (PNCWAVE)", 29.289, -80.803},
    "41076": {"41076", "CHR60WAVE", 32.536, -79.659},
    "41108": {"41108", "Wilmington Harbor, NC - (200)", 33.721, -78.016},
    "41110": {"41110", "Masonboro Inlet, ILM2, NC (150)", 34.143, -77.716},
    "41112": {"41112", "Offshore Fernandina Beach, FL (132)", 30.709, -81.292},
    "41113": {"41113", "Cape Canaveral Nearshore, FL (143)", 28.4, -80.533},
    "41114": {"41114", "Fort Pierce, FL (134)", 27.552, -80.216},
    "41115": {"41115", "Rincon, Puerto Rico (181)", 18.376, -67.28},
    "41117": {"41117", "St. Augustine, FL (194)", 30.0, -81.08},
    "41120": {"41120", "Cape Hatteras East, NC (250)", 35.258, -75.285},
    "41121": {"41121", "Arecibo, Puerto Rico (249)", 18.49, -66.701},
    "41159": {"41159", "Onslow Bay Outer, NC (217)", 34.213, -76.949},
    "42001": {"42001", "MID GULF - 180 nm South of Southwest Pass, LA", 25.926, -89.662},
    "42002": {"42002", "WEST GULF - 207 NM East of Brownsville, TX", 26.055, -93.646},
    "42012": {"42012", "ORANGE BEACH - 44 NM SE of Mobile, AL", 30.06, -87.548},
    "42019": {"42019", "FREEPORT, TX - 60 NM South of Freeport, TX", 27.91, -95.345},
    "42036": {"42036", "WEST TAMPA  - 112 NM WNW of Tampa, FL", 28.501, -84.508},
    "42040": {"42040", "LUKE OFFSHORE TEST PLATFORM - 63 NM South of Dauphin Island, AL", 29.207, -88.237},
    "42055": {"42055", "BAY OF CAMPECHE - 214 NM NE of Veracruz, MX", 22.14, -94.112},
    "42056": {"42056", "Yucatan Basin - 120 NM ESE of Cozumel, MX", 19.82, -84.945},
    "42057": {"42057", "Western Caribbean - 195 NM WSW of Negril, Jamaica", 16.973, -81.575},
    "42058": {"42058", "Central Caribbean - 210 NM SSE of Kingston, Jamaica", 14.844, -75.061},
    "42059": {"42059", "Eastern Caribbean Sea - 180 NM SSW of Ponce, PR", 15.3, -67.483},
    "42060": {"42060", "Caribbean Valley - 63 NM WSW of Montserrat", 16.434, -63.329},
    "42084": {"42084", "Southwest Pass Entrance W, LA (256)", 28.988, -89.649},
    "42085": {"42085", "Southeast of Ponce, PR", 17.87, -66.537},
    "42091": {"42091", "Trinity Shoal, LA (255)", 29.087, -92.506},
    "42097": {"42097", "Pulley Ridge, FL (226)", 25.714, -83.65},
    "42098": {"42098", "Egmont Channel Entrance, FL (214)", 27.59, -82.931},
    "42099": {"42099", "Offshore St. Petersburg, FL (144)", 27.349, -84.275},
    "44005": {"44005", "GULF OF MAINE - 78 NM East of Portsmouth, NH", 43.201, -69.127},
    "44007": {"44007", "PORTLAND - 12 NM Southeast of Portland,ME", 43.525, -70.14},
    "44008": {"44008", "NANTUCKET 54 NM Southeast of Nantucket", 40.496, -69.25},
    "44009": {"44009", "DELAWARE BAY 26 NM Southeast of Cape May, NJ", 38.46, -74.692},
    "44013": {"44013", "BOSTON 16 NM East of Boston, MA", 42.346, -70.651},
    "44014": {"44014", "VIRGINIA BEACH 64 NM East of Virginia Beach, VA", 36.603, -74.837},
    "44018": {"44018", "CAPE COD - 9 NM North of Provincetown, MA", 42.203, -70.154},
    "44020": {"44020", "NANTUCKET SOUND", 41.497, -70.283},
    "44027": {"44027", "Jonesport, ME - 20 NM SE of Jonesport, ME", 44.283, -67.3},
    "44056": {"44056", "Duck FRF, NC", 36.2, -75.714},
    "44065": {"44065", "New York Harbor Entrance - 15 NM SE of Breezy Point , NY", 40.369, -73.703},
    "44078": {"44078", "OOI Irminger Sea Surface Mooring", 59.94, -39.52},
    "44084": {"44084", "Bethany Beach, DE (263)", 38.537, -75.044},
    "44085": {"44085", "Buzzards Bay, MA (260)", 41.387, -71.032},
    "44086": {"44086", "Nags Head, NC (243)", 36.001, -75.421},
    "44087": {"44087", "Thimble Shoal, VA (240)", 37.026, -76.149},
    "44088": {"44088", "Virginia Beach Offshore, VA (171)", 36.614, -74.841},
    "44089": {"44089", "Wallops Island, VA (224)", 37.754, -75.325},
    "44090": {"44090", "Cape Cod Bay, MA (221)", 41.84, -70.329},
    "44091": {"44091", "Barnegat, NJ (209)", 39.768, -73.77},
    "44095": {"44095", "Oregon Inlet, NC (192)", 35.75, -75.33},
    "44097": {"44097", "Block Island, RI  (154)", 40.967, -71.124},
    "44098": {"44098", "Jeffreys Ledge, NH (160)", 42.8, -70.171},
    "44099": {"44099", "Cape Henry, VA (147)", 36.915, -75.722},
    "44100": {"44100", "Duck FRF 26m, NC (430)", 36.258, -75.593},
    "45161": {"45161", "Muskegon Buoy, MI", 43.185, -86.352},
    "45212": {"45212", "North Huron Spotter", 45.351, -82.84},
    "45213": {"45213", "East Superior Spotter", 47.585, -86.585},
    "45214": {"45214", "South Michigan Spotter", 42.674, -87.026},
    "46001": {"46001", "WESTERN GULF OF ALASKA  - 175NM SE of Kodiak, AK", 56.3, -148.018},
    "46005": {"46005", "WEST WASHINGTON - 300NM West of Aberdeen, WA", 46.143, -131.09},
    "46006": {"46006", "SOUTHEAST PAPA - 600NM West of Eureka, CA", 40.764, -137.377},
    "46011": {"46011", "SANTA MARIA - 21NM NW of Point Arguello, CA", 34.936, -120.998},
    "46013": {"46013", "BODEGA BAY - 48NM NW of San Francisco, CA", 38.235, -123.317},
    "46014": {"46014", "PT ARENA - 19NM North of Point Arena, CA", 39.225, -123.98},
    "46022": {"46022", "EEL RIVER - 17NM WSW of Eureka, CA", 40.716, -124.54},
    "46025": {"46025", "Santa Monica Basin - 33NM WSW of Santa Monica, CA", 33.755, -119.045},
    "46026": {"46026", "SAN FRANCISCO - 18NM West of San Francisco, CA", 37.754, -122.839},
    "46027": {"46027", "ST GEORGES - 8 NM NW of Crescent City, CA", 41.84, -124.382},
    "46028": {"46028", "CAPE SAN MARTIN - 55NM West NW of Morro Bay, CA", 35.77, -121.903},
    "46041": {"46041", "CAPE ELIZABETH - 45NM NW of Aberdeen, WA", 47.352, -124.739},
    "46047": {"46047", "TANNER BANK - 121 NM West of San Diego, CA", 32.388, -119.525},
    "46050": {"46050", "STONEWALL BANK - 20NM West of Newport, OR", 44.669, -124.546},
    "46053": {"46053", "EAST SANTA BARBARA  - 12NM Southwest of Santa Barbara, CA", 34.241, -119.839},
    "46054": {"46054", "WEST SANTA BARBARA  38 NM West of Santa Barbara, CA", 34.274, -120.468},
    "46059": {"46059", "WEST CALIFORNIA - 357NM West of San Francisco, CA", 38.069, -129.976},
    "46060": {"46060", "WEST ORCA BAY - 8NM NW of Hinchinbrook Is., AK", 60.571, -146.795},
    "46066": {"46066", "SOUTH KODIAK - 310NM SSW of Kodiak, AK", 52.765, -155.009},
    "46069": {"46069", "SOUTH SANTA ROSA - 14 NM SW of Santa Rosa Island, CA", 33.677, -120.213},
    "46070": {"46070", "SOUTHWEST BERING SEA - 142NM NNE OF ATTU IS, AK", 55.065, 175.268},
    "46071": {"46071", "WESTERN ALEUTIANS - 14NM SOUTH OF AMCHITKA IS, AK", 51.022, 179.784},
    "46072": {"46072", "CENTRAL ALEUTIANS 230 NM SW Dutch Harbor", 51.666, -172.114},
    "46075": {"46075", "SHUMAGIN ISLANDS - 85NM South of Sand Point, AK", 53.969, -160.794},
    "46076": {"46076", "CAPE CLEARE - 17 NM South of Montague Is,  AK", 59.471, -148.009},
    "46077": {"46077", "SHELIKOF STRAIT, AK", 57.869, -154.211},
    "46078": {"46078", "ALBATROSS BANK - 104NM South of Kodiak Is., AK", 55.561, -152.599},
    "46080": {"46080", "PORTLOCK BANK - 76 NM ENE of Kodiak, AK", 57.916, -150.133},
    "46081": {"46081", "Western Prince William Sound", 60.802, -148.283},
    "46082": {"46082", "Cape Suckling - 35 NM SE of Kayak Is, AK", 59.67, -143.353},
    "46083": {"46083", "FAIRWEATHER GROUND - 105 NM West  of Juneau, AK", 58.27, -138.019},
    "46084": {"46084", "CAPE EDGECUMBE - 25NM SSW of Cape Edgecumbe, AK", 56.614, -136.04},
    "46086": {"46086", "SAN CLEMENTE BASIN - 27NM SE Of San Clemente Is, CA", 32.499, -118.052},
    "46088": {"46088", "NEW DUNGENESS - 17 NM NE of Port Angeles, WA", 48.332, -123.179},
    "46097": {"46097", "OOI Newport Shelf", 44.639, -124.304},
    "46098": {"46098", "OOI Waldport Offshore", 44.378, -124.947},
    "46099": {"46099", "OOI Westport Shelf", 46.988, -124.567},
    "46100": {"46100", "OOI Westport Offshore", 46.851, -124.964},
    "46108": {"46108", "Lower Cook Inlet (204)", 59.598, -151.828},
    "46211": {"46211", "Grays Harbor, WA (036)", 46.857, -124.244},
    "46213": {"46213", "Cape Mendocino, CA (094)", 40.295, -124.732},
    "46214": {"46214", "Point Reyes, CA (029)", 37.937, -123.463},
    "46215": {"46215", "Diablo Canyon, CA (076)", 35.204, -120.859},
    "46218": {"46218", "Harvest, CA (071)", 34.452, -120.78},
    "46219": {"46219", "San Nicolas Island, CA (067)", 33.219, -119.872},
    "46221": {"46221", "Santa Monica Bay, CA (028)", 33.86, -118.641},
    "46222": {"46222", "San Pedro, CA (092)", 33.618, -118.317},
    "46224": {"46224", "Oceanside Offshore, CA (045)", 33.178, -117.472},
    "46225": {"46225", "Torrey Pines Outer, CA (100)", 32.933, -117.391},
    "46229": {"46229", "UMPQUA OFFSHORE, OR (139)", 43.772, -124.549},
    "46232": {"46232", "Point Loma South, CA  (191)", 32.517, -117.425},
    "46235": {"46235", "Imperial Beach Nearshore, CA (155)", 32.57, -117.169},
    "46237": {"46237", "San Francisco Bar, CA  (142)", 37.788, -122.634},
    "46239": {"46239", "Point Sur, CA (157)", 36.335, -122.104},
    "46240": {"46240", "Cabrillo Point, Monterey Bay, CA  (158)", 36.626, -121.907},
    "46243": {"46243", "Clatsop Spit, OR (162)", 46.216, -124.128},
    "46244": {"46244", "Humboldt Bay, North Spit, CA (168)", 40.896, -124.357},
    "46251": {"46251", "Santa Cruz Basin, CA (203)", 33.769, -119.565},
    "46253": {"46253", "San Pedro South, CA (213)", 33.576, -118.181},
    "46254": {"46254", "SCRIPPS Nearshore, CA (201)", 32.868, -117.267},
    "46256": {"46256", "Long Beach Channel, CA (215)", 33.7, -118.201},
    "46258": {"46258", "Mission Bay West, CA (220)", 32.749, -117.502},
    "46259": {"46259", "Santa Lucia Escarpment, CA (222)", 34.767, -121.498},
    "46266": {"46266", "Del Mar Nearshore, CA (153)", 32.957, -117.279},
    "46267": {"46267", "Angeles Point, WA (248)", 48.173, -123.607},
    "46268": {"46268", "Topanga Nearshore, CA (103)", 34.022, -118.578},
    "46274": {"46274", "Leucadia Nearshore, CA (262)", 33.062, -117.314},
    "46275": {"46275", "Red Beach Nearshore, CA (264)", 33.29, -117.5},
    "46276": {"46276", "Pajaro Beach, CA (266)", 36.845, -121.825},
    "46277": {"46277", "Green Beach Offshore, CA (271)", 33.336, -117.659},
    "46278": {"46278", "Tillamook Bay South Jetty, OR (270)", 45.561, -123.991},
    "46279": {"46279", "Pajaro Beach South, CA (267)", 36.838, -121.82},
    "51000": {"51000", "NORTHERN HAWAII ONE - 245NM NE of Honolulu HI", 23.528, -153.792},
    "51001": {"51001", "NORTHWESTERN HAWAII ONE - 188 NM NW of Kauai Island, HI", 24.451, -162.008},
    "51003": {"51003", "WESTERN  HAWAII - 205 NM SW of Honolulu, HI", 19.196, -160.639},
    "51004": {"51004", "SOUTHEAST HAWAII - 205 NM Southeast of Hilo, HI", 17.538, -152.23},
    "51101": {"51101", "NORTHWESTERN HAWAII TWO - 186 NM NW of Kauai Is., HI", 24.359, -162.081},
    "51201": {"51201", "Waimea Bay, HI (106)", 21.671, -158.118},
    "51202": {"51202", "Mokapu Point, HI (098)", 21.417, -157.68},
    "51205": {"51205", "Pauwela, Maui, HI (187)", 21.018, -156.425},
    "51207": {"51207", "Kaneohe Bay, HI (198)", 21.477, -157.752},
    "51210": {"51210", "Kaneohe Bay, WETS, HI (225)", 21.477, -157.757},
    "51211": {"51211", "Pearl Harbor Entrance, HI (233)", 21.297, -157.959},
    "51212": {"51212", "Barbers Point, Kalaeloa, HI (238)", 21.323, -158.149},
    "52200": {"52200", "Ipan, Guam (121)", 13.354, 144.788},
    "52201": {"52201", "Kalo, Majuro, Marshall Islands (163)", 7.079, 171.384},
    "52211": {"52211", "Tanapag, Saipan, NMI (197)", 15.268, 145.662},
    "52212": {"52212", "Ngaraard, Babeldaob, Palau (219)", 7.63, 134.671},
    "62107": {"62107", "Sevenstones Lightship", 50.102, -6.1},
    "62127": {"62127", "Cleeton AWS", 54.0, 0.7},
    "62130": {"62130", "Brae A", 58.7, 1.3},
    "62144": {"62144", "Clipper AWS", 53.4, 1.7},
    "62145": {"62145", "North Sea", 53.102, 2.8},
    "62146": {"62146", "Lomond AWS", 57.2, 2.1},
    "62149": {"62149", "West Sole \"A\" AWS", 53.7, 1.1},
    "62165": {"62165", "Ravenspurn North AWS", 54.0, 1.1},
    "62170": {"62170", "F3 Light Vessel", 51.24, 2.0},
    "62304": {"62304", "Sandettie Lightship", 51.102, 1.8},
    "62305": {"62305", "Greenwich Lightship", 50.4, 0.0},
    "63110": {"63110", "Beryl A AWS", 59.5, 1.5},
    "63112": {"63112", "Cormorant AWS", 61.1, 1.0},
    "63115": {"63115", "Magnus AWS", 61.6, 1.3},
}

var directionMap = map[string]float64{
    "N":   0,
    "NNE": 22.5,
    "NE":  45,
    "ENE": 67.5,
    "E":   90,
    "ESE": 112.5,
    "SE":  135,
    "SSE": 157.5,
    "S":   180,
    "SSW": 202.5,
    "SW":  225,
    "WSW": 247.5,
    "W":   270,
    "WNW": 292.5,
    "NW":  315,
    "NNW": 337.5,
}

type ForecastSummary struct {
	Date       string
	DateAbv    string
	Condition  string
	WaveHeight string
}
type SwellReport struct {
    StationId string
    Date string
    PrimaryWaveHeight string
    PrimaryPeriod string
    PrimaryDegrees string
    SecondaryWaveHeight string
    SecondaryPeriod string
    SecondaryDegrees string
    Steepness string
}

type WindReport struct {
    StationId string
    Date string
    WindSpeed string
    WindGust string
    WindDir string
    AirTemp string
    WaterTemp string
}



type CacheItem struct {
    Data      interface{}
    Timestamp time.Time
}

type Cache struct {
    items map[string]CacheItem
    mutex sync.RWMutex
}

func NewCache() *Cache {
    return &Cache{
        items: make(map[string]CacheItem),
    }
}

func (c *Cache) Set(key string, value interface{}) {
    c.mutex.Lock()
    defer c.mutex.Unlock()
    c.items[key] = CacheItem{
        Data:      value,
        Timestamp: time.Now(),
    }
}

func (c *Cache) Get(key string) (interface{}, bool) {
    c.mutex.RLock()
    defer c.mutex.RUnlock()
    item, found := c.items[key]
    if !found {
        return nil, false
    }
    return item.Data, true
}

func (c *Cache) ClearEvery(d time.Duration) {
    ticker := time.NewTicker(d)
    go func() {
        for {
            <-ticker.C
            c.mutex.Lock()
            c.items = make(map[string]CacheItem)
            c.mutex.Unlock()
        }
    }()
}


func getDate(data string) string {
    lines := strings.Split(data, "\n")
    dateline := lines[2]
    parts := strings.Fields(dateline)
    return parts[2]
}

func parseBullFile(data string) ([]ForecastRow, error) {
    // Parse the data from the bull file
    forecast := []ForecastRow{}
    lines := strings.Split(data, "\n")

    lines = lines[6:391]

    for _, line := range lines {
        // break if the line starts with "n :"
        if strings.HasPrefix(line, "n :") {
            break
        }
        // use regex to get the first 3 elements in between each set of | 
        // remove all "|" and replace with space
        line = strings.ReplaceAll(line, "|", " ")
        line = strings.ReplaceAll(line, "*", "")
        // split by space
        parts := strings.Fields(line)
        // if the the fifth value is equal to 1, remove it
        // check of parts[4] is a whole integer

        if len(parts) > 4 && (parts[4] == "1" || parts[4] == "2" || parts[4] == "3" || parts[4] == "4" || parts[4] == "5") {
            parts = append(parts[:4], parts[5:]...)
        }

        if len(parts) < 7 {
            continue
        }
        if len(parts) < 8 {
            forecast = append(forecast, ForecastRow{
                Date: parts[0] + " " + parts[1],
                PrimaryWaveHeight: parts[4],
                PrimaryPeriod: parts[5],
                PrimaryDegrees: parts[6],
            })
        } else if len(parts) < 11 {
            forecast = append(forecast, ForecastRow{
                Date: parts[0] + " " + parts[1],
                PrimaryWaveHeight: parts[4],
                PrimaryPeriod: parts[5],
                PrimaryDegrees: parts[6],
                SecondaryWaveHeight: parts[7],
                SecondaryPeriod: parts[8],
                SecondaryDegrees: parts[9],
            })
        } else if len(parts) < 14 {
            forecast = append(forecast, ForecastRow{
                Date: parts[0] + " " + parts[1],
                PrimaryWaveHeight: parts[4],
                PrimaryPeriod: parts[5],
                PrimaryDegrees: parts[6],
                SecondaryWaveHeight: parts[7],
                SecondaryPeriod: parts[8],
                SecondaryDegrees: parts[9],
                TertiaryWaveHeight: parts[10],
                TertiaryPeriod: parts[11],
                TertiaryDegrees: parts[12],
            })
        } else {
            forecast = append(forecast, ForecastRow{
                Date: parts[0] + " " + parts[1],
                PrimaryWaveHeight: parts[4],
                PrimaryPeriod: parts[5],
                PrimaryDegrees: parts[6],
                SecondaryWaveHeight: parts[7],
                SecondaryPeriod: parts[8],
                SecondaryDegrees: parts[9],
                TertiaryWaveHeight: parts[10],
                TertiaryPeriod: parts[11],
                TertiaryDegrees: parts[12],
                QuaternaryWaveHeight: parts[13],
                QuaternaryPeriod: parts[14],
                QuaternaryDegrees: parts[15],
            })

        }
    }
    return forecast, nil
}

func getSwellReport(stationId string) (SwellReport, error) {
    if stationId == "" {
        return SwellReport{}, nil
    }
    client := resty.New()
    resp, err := client.R().SetHeader("X-Requested-With", "XMLHttpRequest").Get("https://www.ndbc.noaa.gov/data/realtime2/" + stationId + ".spec")
    if err != nil {
        return SwellReport{}, err
    }
    // skip the first 2 lines, then we only need the first line of data
    lines := strings.Split(resp.String(), "\n")
    line := lines[2]
    // split by space
    parts := strings.Fields(line)

    secondaryDegrees := parts[10]
    if degrees, ok := directionMap[secondaryDegrees]; ok {
        secondaryDegrees = fmt.Sprintf("%.0f", degrees)
    }

    report := SwellReport{
        StationId: stationId,
        Date: parts[0] + "/" + parts[1] + "/" + parts[2] + " " + parts[3] + ":" + parts[4],
        PrimaryWaveHeight: parts[5],
        PrimaryPeriod: parts[7],
        PrimaryDegrees: parts[14],
        SecondaryWaveHeight: parts[8],
        SecondaryPeriod: parts[9],
        SecondaryDegrees: secondaryDegrees,
        Steepness: parts[12],
    }
    return report, nil
}

func getWindReport(stationId string) (WindReport, error) {
    if stationId == "" {
        return WindReport{}, nil
    }

    client := resty.New()
    resp, err := client.R().SetHeader("X-Requested-With", "XMLHttpRequest").Get("https://www.ndbc.noaa.gov/data/realtime2/" + stationId + ".txt")
    if err != nil {
        return WindReport{}, err
    }
    lines := strings.Split(resp.String(), "\n")
    if len(lines) < 3 {
        return WindReport{}, fmt.Errorf("insufficient data in response")
    }
    line := lines[2]
    // split by space
    parts := strings.Fields(line)
    if len(parts) < 15 {
        return WindReport{}, fmt.Errorf("insufficient data in response line")
    }
    report := WindReport{
        StationId: stationId,
        Date: parts[0] + " " + parts[1] + " " + parts[2] + " " + parts[3],
        WindSpeed: parts[6],
        WindGust: parts[7],
        WindDir: parts[5],
        AirTemp: parts[13],
        WaterTemp: parts[14],
    }
    return report, nil
}

// doing nothing rn
func getTides(c *gin.Context) {
    tmpl, err := template.ParseFiles("tides.html", "templates/tides.html")
    // Execute the template with the prediction data
    htmlBuffer := new(bytes.Buffer)
    err = tmpl.ExecuteTemplate(htmlBuffer, "tides.html", nil)
    if err != nil {
        log.Println(err)
        c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
        return
    }

    c.Header("Content-Type", "text/html; charset=utf-8")
    c.String(http.StatusOK, htmlBuffer.String())
}

func getForecast(c *gin.Context, cache *Cache, stationId string) (map[string]interface{}, error) {
    // stationId := c.Param("stationId")

    client := resty.New()
    now := time.Now().UTC()

    var returndata map[string]interface{}
    if cachedData, found := cache.Get(stationId); found {
        returndata, _ = cachedData.(map[string]interface{})
        return returndata, nil
    }

    var data string
    var err error
    for i := 0; i < 3; i++ {
        formattedDate := now.Format("20060102")
        formattedTime := now.Hour() / 6 * 6
        url := fmt.Sprintf("https://nomads.ncep.noaa.gov/pub/data/nccf/com/gfs/prod/gfs.%s/%02d/wave/station/bulls.t%02dz/gfswave.%s.bull", formattedDate, formattedTime, formattedTime, stationId)

        resp, err := client.R().SetHeader("X-Requested-With", "XMLHttpRequest").Get(url)
        if err == nil && resp.StatusCode() == http.StatusOK {
            data = resp.String()
            break
        }
        now = now.Add(-6 * time.Hour)
    }


    parsedData, err := parseBullFile(data)
    if err != nil {
        return nil, fmt.Errorf("failed to parse bull file: %w", err)
    }

    date := now.Format("2006010215")[:10] // Format as YYYYMMDDHH
    // date := getDate(data)

    returndata = map[string]interface{}{
        "forecast":    parsedData,
        "date":        date,
    }

    cache.Set(stationId, returndata)
    return returndata, nil
}

func formatDate(date string) string {
	parsedDate, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return parsedDate.Format("Mon 1/2")
}

func calculateAverageWaveHeight(forecast []ForecastRow) float64 {
	total := 0.0
	count := 0
	for _, row := range forecast {
		height, err := strconv.ParseFloat(row.PrimaryWaveHeight, 64)
		if err == nil {
			total += height
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

func determineCondition(avgWaveHeight float64) string {
	heightInFeet := avgWaveHeight * 3.28084
	if heightInFeet < 0.5 {
		return "poor"
	} else if heightInFeet < 1.5 {
		return "fair"
	}
	return "good"
}


func sortForecastSummary(summary []ForecastSummary) {
	sort.Slice(summary, func(i, j int) bool {
		return summary[i].Date < summary[j].Date
	})
}

func renderForecastSummary(w http.ResponseWriter, cache *Cache,  uid string, db *sql.DB) {
	var buoys []struct {
		Buoy
		Summary []ForecastSummary
	}

	if uid == "" {
		http.Error(w, "UID is required", http.StatusBadRequest)
		return
	}

	//buoyIDs := []string{"46221", "46232"}
	buoyIDs, err := getBuoysForUser(db, uid)
	if err != nil {
	    http.Error(w, "no favorites", http.StatusBadRequest)
	    return
	}

	for _, buoyID := range buoyIDs {
		forecastData, err := getForecast(nil, cache, buoyID)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error fetching data for buoy %s: %v", buoyID, err), http.StatusInternalServerError)
			return
		}

		forecast, ok := forecastData["forecast"].([]ForecastRow)
		if !ok {
			http.Error(w, fmt.Sprintf("Invalid forecast data for buoy %s", buoyID), http.StatusInternalServerError)
			return
		}

		initialDate, ok := forecastData["date"].(string)
		if !ok {
			http.Error(w, fmt.Sprintf("Invalid initial date for buoy %s", buoyID), http.StatusInternalServerError)
			return
		}

		baseTime, err := time.Parse("2006010215", initialDate)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error parsing initial date for buoy %s: %v", buoyID, err), http.StatusInternalServerError)
			return
		}

		groupedForecast := make(map[string][]ForecastRow)
		for i, row := range forecast {
			forecastTime := baseTime.Add(time.Duration(i) * time.Hour)
			day := forecastTime.Format("2006-01-02")
			groupedForecast[day] = append(groupedForecast[day], row)
		}

		var summary []ForecastSummary
		for day, rows := range groupedForecast {
			avgWaveHeight := calculateAverageWaveHeight(rows)
			condition := determineCondition(avgWaveHeight)
			waveHeightFeet := fmt.Sprintf("%.1fft", avgWaveHeight*3.28084)

			summary = append(summary, ForecastSummary{
				Date:       day,
				DateAbv:    formatDate(day),
				Condition:  condition,
				WaveHeight: waveHeightFeet,
			})
		}

		// Sort the summary slice by date
		sortForecastSummary(summary)

		buoyName := fmt.Sprintf("Buoy %s", buoyID)
		if location, ok := BuoyLocations[buoyID]; ok {
			buoyName = location.Name
		}
		buoys = append(buoys, struct {
			Buoy
			Summary []ForecastSummary
		}{
			Buoy: Buoy{
				ID:       buoyID,
				Name:     buoyName,
				Forecast: forecastData,
			},
			Summary: summary,
		})
	}

	tmpl := template.Must(template.ParseFiles("templates/forecastsummary.html"))
	err = tmpl.ExecuteTemplate(w, "forecastsummary", struct {
		Buoys []struct {
			Buoy
			Summary []ForecastSummary
		}
	}{Buoys: buoys})

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}


var (
	authClient *auth.Client
)

// Open the database connection
func openDatabase() *sql.DB {
    db, err := sql.Open("sqlite3", "../open-swells-db/main.db")
    if err != nil {
        log.Fatalf("Failed to connect to database: %v", err)
    }

    if err := db.Ping(); err != nil {
        log.Fatalf("Failed to ping database: %v", err)
    }

    return db
}

func insertUserBuoy(db *sql.DB, uid, buoyID string) error {
    // Insert user if not exists
    _, err := db.Exec(`INSERT OR IGNORE INTO users (uid) VALUES (?)`, uid)
    if err != nil {
        return err
    }

    // Insert the user-buoy mapping
    _, err = db.Exec(`INSERT INTO user_buoys (uid, buoy_id) VALUES (?, ?)`, uid, buoyID)
    return err
}

func deleteUserBuoy(db *sql.DB, uid, buoyID string) error {
    // Delete the user-buoy mapping
    _, err := db.Exec(`DELETE FROM user_buoys WHERE uid = ? AND buoy_id = ?`, uid, buoyID)
    return err
}

func getBuoysForUser(db *sql.DB, uid string) ([]string, error) {
    rows, err := db.Query(`SELECT buoy_id FROM user_buoys WHERE uid = ?`, uid)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var buoys []string
    for rows.Next() {
        var buoyID string
        if err := rows.Scan(&buoyID); err != nil {
            return nil, err
        }
        buoys = append(buoys, buoyID)
    }
    return buoys, nil
}

func verifyUserID(uid string) error {
    _, err := authClient.GetUser(context.Background(), uid)
    return err
}


func main() {
    // start firebase auth
    opt := option.WithCredentialsFile("/home/evan/Downloads/open-swells-89714-keys.json")
    app, err := firebase.NewApp(context.Background(), nil, opt)
    if err != nil {
       panic(fmt.Sprintf("error initializing app: %v", err))
    }

    authClient, err = app.Auth(context.Background())
    if err != nil {
        panic(fmt.Sprintf("Error getting Auth client: %v", err))
    }

    //start db
    db := openDatabase()
    defer db.Close()

    // gin.SetMode(gin.ReleaseMode)
    router := gin.Default()
    cache := NewCache()

    cache.ClearEvery(6 * time.Hour)

    router.Static("/static", "./static")


    router.ForwardedByClientIP = true
    //router.SetTrustedProxies([]string{"127.0.0.1","192.168.1.250", "192.168.1.1"})
    trustedProxies := []string {
        "2a01:7e03::f03c:94ff:fee7:167d", 
        "127.0.0.1",                     
        "::1",
    }
    err = router.SetTrustedProxies(trustedProxies)
    if err != nil {
        log.Fatalf("failed to set proxies: %v",  err) 
    }

    // main route
    router.GET("/map", func(c *gin.Context) {

        //------------------- Use an Empty report for the opening page ----------------
        windreport, err := getWindReport("")
        swellreport, err := getSwellReport("")

        forecastdata, err := getForecast(c, cache, "46221")
        // print foreacsta dat

        var returndata map[string]interface{}
        returndata = map[string]interface{}{"forecastdata": forecastdata, "windreport": windreport, "swellreport": swellreport}


        tmpl, err := template.ParseFiles("pages/today.html", "templates/report_small.html", "templates/forecastsummary.html")
        htmlBuffer := new(bytes.Buffer)
        err = tmpl.ExecuteTemplate(htmlBuffer, "today.html", returndata)

        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
            return
        } 

        c.Header("Content-Type", "text/html; charset=utf-8")
        c.String(http.StatusOK, htmlBuffer.String())

    })

    router.GET("/", func(c *gin.Context) {
        tmpl, err := template.ParseFiles("pages/landing.html")
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
            return
        }

        err = tmpl.Execute(c.Writer, nil)
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
            return
        }

        c.Header("Content-Type", "text/html; charset=utf-8")
        c.Status(http.StatusOK)
    })

    router.GET("/report/:stationId", func(c *gin.Context) {
        stationId := c.Param("stationId")
        windreport, err := getWindReport(stationId)
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get wind report"})
            return
        }
        swellreport, err := getSwellReport(stationId)
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get swell report"})
            return
        }

        returndata := map[string]interface{}{
            "windreport":  windreport,
            "swellreport": swellreport,
        }

        tmpl, err := template.ParseFiles("templates/report.html")
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse template"})
            return
        }

        htmlBuffer := new(bytes.Buffer)
        err = tmpl.ExecuteTemplate(htmlBuffer, "report", returndata)
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to execute template"})
            return
        }

        c.Header("Content-Type", "text/html; charset=utf-8")
        c.String(http.StatusOK, htmlBuffer.String())
    })

    router.GET("/report_small/:stationId", func(c *gin.Context) {
        stationId := c.Param("stationId")
        windreport, err := getWindReport(stationId)
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get wind report"})
            return
        }
        swellreport, err := getSwellReport(stationId)
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get swell report"})
            return
        }

        returndata := map[string]interface{}{
            "windreport":  windreport,
            "swellreport": swellreport,
        }

        tmpl, err := template.ParseFiles("templates/report_small.html")
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse template"})
            return
        }

        htmlBuffer := new(bytes.Buffer)
        err = tmpl.ExecuteTemplate(htmlBuffer, "report_small", returndata)
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to execute template"})
            return
        }

        c.Header("Content-Type", "text/html; charset=utf-8")
        c.String(http.StatusOK, htmlBuffer.String())
    })

    router.GET("/forecast/:stationId", func(c *gin.Context) {
        stationId := c.Param("stationId")

        swellreport, err := getSwellReport(stationId)
        windreport, err := getWindReport(stationId)

        forecastdata, err := getForecast(c, cache, stationId)

        // Get buoy name from BuoyLocations map
        buoyName := fmt.Sprintf("Buoy %s", stationId) // default name if not found
        if location, ok := BuoyLocations[stationId]; ok {
            buoyName = location.Name
        }

        returndata := map[string]interface{}{
            "forecastdata":    forecastdata,
            "swellreport": swellreport,
            "windreport":  windreport,
            "buoyName":    buoyName,
        }

        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get forecast data"})
            return
        }

        tmpl, err := template.ParseFiles("pages/buoy.html", "templates/forecast.html", "templates/report.html")
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse template"})
            return
        }

        // Execute the template with the prediction data
        htmlBuffer := new(bytes.Buffer)
        err = tmpl.ExecuteTemplate(htmlBuffer, "buoy.html", returndata)
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to execute template"})
            return
        }

        c.Header("Content-Type", "text/html; charset=utf-8")
        c.String(http.StatusOK, htmlBuffer.String())
    })

    // this just sends the data
    router.GET("realtime/:stationId", func(c *gin.Context) {
        stationId := c.Param("stationId")
        url := "https://www.ndbc.noaa.gov/data/realtime2/" + stationId + ".spec"

        resp, err := http.Get(url)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch data"})
            return
        }
        defer resp.Body.Close()

        c.Header("Content-Type", resp.Header.Get("Content-Type"))
        c.Status(resp.StatusCode)
        io.Copy(c.Writer, resp.Body)

    })

    // this also sends data, no template
    router.GET("realtime/wind/:stationId", func(c *gin.Context) {
        stationId := c.Param("stationId")
        url := "https://www.ndbc.noaa.gov/data/realtime2/" + stationId + ".txt"

        resp, err := http.Get(url)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch data"})
            return
        }
        defer resp.Body.Close()

        c.Header("Content-Type", resp.Header.Get("Content-Type"))
        c.Status(resp.StatusCode)
        io.Copy(c.Writer, resp.Body)

    })

    router.GET("/about", func(c *gin.Context) {
        tmpl, err := template.ParseFiles("pages/about.html")

        err = tmpl.Execute(c.Writer, nil)
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
            return
        }

        c.Header("Content-Type", "text/html; charset=utf-8")
        c.Status(http.StatusOK)
    })

    router.POST("/auth", func (c *gin.Context) {
        var req struct {
            IDToken string `json:"idToken"`
        }

        if err := c.BindJSON(&req); err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
            return
        }

        token, err := authClient.VerifyIDToken(context.Background(), req.IDToken)
        if err != nil {
            c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid ID token"})
            return
        }

        user, err := authClient.GetUser(context.Background(), token.UID)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Error fetching user"})
            return
        }

        c.JSON(http.StatusOK, gin.H{
            "name":  user.DisplayName,
            "email": user.Email,
        })
    })

    router.GET("/forecast-summary", func(c *gin.Context) {
        uid := c.Query("uid")
		renderForecastSummary(c.Writer,  cache, uid, db)
	})

    router.GET("/add", func(c *gin.Context) {
        uid := c.Query("uid")
        buoyID := c.Query("buoyid")

        if uid == "" || buoyID == "" {
            c.JSON(http.StatusBadRequest, gin.H{"error": "Both uid and buoyid are required"})
            return
        }

        // Verify the user ID with Firebase
        err := verifyUserID(uid)
        if err != nil {
            log.Printf("Failed to verify user ID: %v", err)
            c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid user ID"})
            return
        }

        err = insertUserBuoy(db, uid, buoyID)
        if err != nil {
            errorMsg := fmt.Sprintf("Failed to insert user buoy: %v", err)
            log.Println(errorMsg)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to insert user buoy"})
            return
        }

        c.JSON(http.StatusOK, gin.H{"message": "User buoy added successfully"})
    })

    router.GET("/delete", func(c *gin.Context) {
        uid := c.Query("uid")
        buoyID := c.Query("buoyid")

        if uid == "" || buoyID == "" {
            c.JSON(http.StatusBadRequest, gin.H{"error": "Both uid and buoyid are required"})
            return
        }

        // Verify the user ID with Firebase
        err := verifyUserID(uid)
        if err != nil {
            log.Printf("Failed to verify user ID: %v", err)
            c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid user ID"})
            return
        }

        err = deleteUserBuoy(db, uid, buoyID)
        if err != nil {
            errorMsg := fmt.Sprintf("Failed to delete user buoy: %v", err)
            log.Println(errorMsg)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete user buoy"})
            return
        }

        c.JSON(http.StatusOK, gin.H{"message": "User buoy deleted successfully"})
    })

    router.GET("/get-user-buoys", func(c *gin.Context) {
        uid := c.Query("uid")

        if uid == "" {
            c.JSON(http.StatusBadRequest, gin.H{"error": "uid is required"})
            return
        }

        // Verify the user ID with Firebase
        err := verifyUserID(uid)
        if err != nil {
            log.Printf("Failed to verify user ID: %v", err)
            c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid user ID"})
            return
        }

        buoys, err := getBuoysForUser(db, uid)
        if err != nil {
            log.Printf("Failed to get user buoys: %v", err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get user buoys"})
            return
        }

        c.JSON(http.StatusOK, gin.H{"buoys": buoys})
    })

    router.GET("/heatmap", func(c *gin.Context) {
        // Open connection to wave_forecast database
        waveDB, err := sql.Open("sqlite3", "../grib-parse-collect/pythonscripts/wave_forecast.db")
        if err != nil {
            log.Printf("Failed to connect to wave_forecast database: %v", err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Database connection failed"})
            return
        }
        defer waveDB.Close()

        // Query the database for lat, lon, and wave_height
        rows, err := waveDB.Query(`
            SELECT latitude, longitude, wave_height 
            FROM wave_forecast 
        `)
        if err != nil {
            log.Printf("Failed to query database: %v", err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Query failed"})
            return
        }
        defer rows.Close()

        // Create slice to store the data points
        var heatmapData [][]float64

        // Iterate through results and format data
        for rows.Next() {
            var lat, lon, height float64
            if err := rows.Scan(&lat, &lon, &height); err != nil {
                log.Printf("Error scanning row: %v", err)
                continue
            }
            heatmapData = append(heatmapData, []float64{lat, lon, height})
        }

        c.JSON(http.StatusOK, heatmapData)
    }) 

    router.Run(":8081")
}
