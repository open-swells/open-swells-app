package main

import (
    "strings"
    "sync"
    "time"
    "bytes"
	"html/template"
    "fmt"
	"log"
	"net/http"
	"github.com/gin-gonic/gin"
    "github.com/go-resty/resty/v2"
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

func parseBullFileTest(data string) ([]ForecastRow, error) {
    forecast := []ForecastRow{}
    lines := strings.Split(data, "\n")
    lines = lines[6:391]
    for _, line := range lines {
        if strings.HasPrefix(line, "n :") {
            break
        }
        // split line by | 
        lineForecast := ForecastRow{}
        sections := strings.Split(line, "|")
        for i, section := range sections {
            if i == 2 {
                // go to next iteration in loop
                continue
            }
            if i == 1 && len(section) > 1 {
                // split by space, add to forecast
                parts := strings.Fields(section)
                lineForecast.Date = parts[0] + " " + parts[1]
            } else if i == 3 && len(section) > 2 { 
                parts := strings.Fields(section)
                if len(parts) < 3 {
                    continue
                }
                lineForecast.PrimaryWaveHeight = parts[0]
                lineForecast.PrimaryPeriod = parts[1]
                lineForecast.PrimaryDegrees = parts[2]
            } else if i == 4 && len(section) > 2 {
                parts := strings.Fields(section)
                if len(parts) < 3 {
                    continue
                }
                lineForecast.SecondaryWaveHeight = parts[0]
                lineForecast.SecondaryPeriod = parts[1]
                lineForecast.SecondaryDegrees = parts[2]
            } else if i == 5 && len(section) > 2 {
                parts := strings.Fields(section)
                if len(parts) < 3 {
                    continue
                }
                lineForecast.TertiaryWaveHeight = parts[0]
                lineForecast.TertiaryPeriod = parts[1]
                lineForecast.TertiaryDegrees = parts[2]
            } else if i == 6 && len(section) > 2 {
                parts := strings.Fields(section)
                if len(parts) < 3 {
                    continue
                }
                lineForecast.QuaternaryWaveHeight = parts[0]
                lineForecast.QuaternaryPeriod = parts[1]
                lineForecast.QuaternaryDegrees = parts[2]
            } else {
                continue
            }
                
        }
        if len(lineForecast.Date) > 0 {
            forecast = append(forecast, lineForecast)
        }
    }
    return forecast, nil
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
    client := resty.New()
    resp, err := client.R().SetHeader("X-Requested-With", "XMLHttpRequest").Get("https://www.ndbc.noaa.gov/data/realtime2/" + stationId + ".txt")
    if err != nil {
        return WindReport{}, err
    }
    lines := strings.Split(resp.String(), "\n")
    line := lines[2]
    // split by space
    parts := strings.Fields(line)
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

func getForecast(c *gin.Context, cache *Cache) {
    stationId := c.Param("stationId")

    client := resty.New()
    now := time.Now().UTC()


    var returndata map[string]interface{}
    if cachedData, found := cache.Get(stationId); found {
        returndata, _ = cachedData.(map[string]interface{})
    } else {
        var data string;
        var err error;
        for i:= 0; i < 3; i++ {

            formattedDate := now.Format("20060102")
            formattedTime := now.Hour() / 6 * 6
            url := fmt.Sprintf("https://nomads.ncep.noaa.gov/pub/data/nccf/com/gfs/prod/gfs.%s/%02d/wave/station/bulls.t%02dz/gfswave.%s.bull", formattedDate, formattedTime, formattedTime, stationId)
            fmt.Println(url)

            resp, err := client.R().SetHeader("X-Requested-With", "XMLHttpRequest").Get(url)
            if err == nil && resp.StatusCode() == http.StatusOK {
                data,err = resp.String(), nil
                break
            } else { 
                now = now.Add(-6 * time.Hour)
            }
        }

        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
            return
        }

        parsedData, err := parseBullFile(data)

        date := getDate(data)
        windreport, err := getWindReport(stationId)
        swellreport, err := getSwellReport(stationId)


        returndata = map[string]interface{}{"forecast": parsedData, "date": date, "windreport": windreport, "swellreport": swellreport}

        cache.Set(stationId, returndata)
    }

    tmpl, err := template.ParseFiles("buoy.html", "templates/forecast.html", "templates/report.html")
    // Execute the template with the prediction data
    htmlBuffer := new(bytes.Buffer)
    // err = tmpl.Execute(htmlBuffer, returndata)
    err = tmpl.ExecuteTemplate(htmlBuffer, "buoy.html", returndata)
    if err != nil {
        log.Println(err)
        c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
        return
    }

    c.Header("Content-Type", "text/html; charset=utf-8")
    c.String(http.StatusOK, htmlBuffer.String())
}



func main() {
    gin.SetMode(gin.ReleaseMode)
    router := gin.Default()
    cache := NewCache()

    cache.ClearEvery(6 * time.Hour)

    router.ForwardedByClientIP = true
    //router.SetTrustedProxies([]string{"127.0.0.1","192.168.1.250", "192.168.1.1"})

    // create a string of test data

   // testdata := `|  9 19 | 0.75  6   |   0.47  8.6  99 |   0.36 11.9 112 |   0.24 14.6  13 |   0.24 14.3 111 |   0.23 12.5  16 |   0.18 17.2  10 |
   //  |  9 20 | 0.76  6   |   0.50  8.6 100 |   0.31 11.6 111 |   0.30 13.8 114 |   0.23 14.6  13 |   0.22 12.5  16 |   0.20 16.9  11 |
   //  |  9 21 | 0.82  7 1 |   0.56  6.7 102 |   0.31 13.5 113 |   0.28 11.3 112 |   0.23 14.5  14 |  |   |`
   // result, err := parseBullFileTest(testdata)
   // fmt.Println(result)
   // if err != nil {
   //     fmt.Println(err)
   // }

    // main route
    router.GET("/", func(c *gin.Context) {
        tmpl, err := template.ParseFiles("today.html")

        err = tmpl.Execute(c.Writer, nil)
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
            return
        }

        c.Header("Content-Type", "text/html; charset=utf-8")
        c.Status(http.StatusOK)
    })

    router.GET("/forecast/:stationId", func(c *gin.Context) {
        getForecast(c, cache)
    })

    router.Run()
}
