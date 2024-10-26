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
	"log"
	"net/http"
    "strconv"
    "sort"

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

type Buoy struct {
    ID       string
    Name     string
    Forecast map[string]interface{}
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
    report := SwellReport{
        StationId: stationId,
        Date: parts[0] + " " + parts[1] + " " + parts[2] + " " + parts[3],
        PrimaryWaveHeight: parts[5],
        PrimaryPeriod: parts[7],
        PrimaryDegrees: parts[14],
        SecondaryWaveHeight: parts[8],
        SecondaryPeriod: parts[9],
        SecondaryDegrees: parts[10],
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

func renderForecastSummary(w http.ResponseWriter, cache *Cache) {
	buoyIDs := []string{"46221", "46232"}
	var buoys []struct {
		Buoy
		Summary []ForecastSummary
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

		buoys = append(buoys, struct {
			Buoy
			Summary []ForecastSummary
		}{
			Buoy: Buoy{
				ID:       buoyID,
				Name:     fmt.Sprintf("Buoy %s", buoyID),
				Forecast: forecastData,
			},
			Summary: summary,
		})
	}

	tmpl := template.Must(template.ParseFiles("templates/forecastsummary3.html"))
	err := tmpl.ExecuteTemplate(w, "forecastsummary3", struct {
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


func main() {
    // start firebase auth
    opt := option.WithCredentialsFile("/Users/evancoons/Downloads/open-swells-89714-firebase-adminsdk-ghfog-cab6d41e1d.json")
    app, err := firebase.NewApp(context.Background(), nil, opt)
    if err != nil {
       panic(fmt.Sprintf("error initializing app: %v", err))
    }

    authClient, err = app.Auth(context.Background())
    if err != nil {
        panic(fmt.Sprintf("Error getting Auth client: %v", err))
    }

    // gin.SetMode(gin.ReleaseMode)
    router := gin.Default()
    cache := NewCache()

    cache.ClearEvery(6 * time.Hour)

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
    router.GET("/", func(c *gin.Context) {

        //------------------- Use an Empty report for the opening page ----------------
        windreport, err := getWindReport("")
        swellreport, err := getSwellReport("")

        forecastdata, err := getForecast(c, cache, "46221")
        // print foreacsta dat

        var returndata map[string]interface{}
        returndata = map[string]interface{}{"forecastdata": forecastdata, "windreport": windreport, "swellreport": swellreport}


        tmpl, err := template.ParseFiles("pages/today.html", "templates/report.html", "templates/forecastsummary.html")
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

    router.GET("/forecast/:stationId", func(c *gin.Context) {
        stationId := c.Param("stationId")
        forecastdata, err := getForecast(c, cache, stationId)
        swellreport, err := getSwellReport(stationId)
        windreport, err := getWindReport(stationId)


        returndata := map[string]interface{}{
            "forecastdata":    forecastdata,
            "swellreport": swellreport,
            "windreport":  windreport,
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
		renderForecastSummary(c.Writer,  cache)
	})

    router.Run(":8081")
}
