package main

import (
    //"os"
    //"strconv"
    "strings"
    //"bufio"
    "time"
    "bytes"
	//"database/sql"
	"html/template"
    "fmt"
	"log"
	"net/http"
	"github.com/gin-gonic/gin"
    "github.com/go-resty/resty/v2"
    //_ "github.com/mattn/go-sqlite3"
    //_ "github.com/libsql/libsql-client-go/libsql"
    "math"
)

type Prediction struct {
    Title string
	WaveHeight float64
	DirectionDegrees  float64
    DirectionLabel string
	Period     float64
}

type ValuesStruct struct { 
    WvHT float64
    SwH float64
    SwP float64
    WWH float64
    WWP float64
    SwD string
    WWD string
    STEEPNESS string
    APD float64
    MWD float64
    Date string
    UpdateDate string
}

type CurrentReport struct {
    Labels []string
    Values ValuesStruct
    //Values []string
}

type MapData struct {
    Datetime string
    Latitude float64
    Longitude float64
    Waveheight float64
    Period float64
    Direction float64
    Datapointsaverage int
}

type CurrentWeather struct { 
    Temperature string
    WindSpeed string
    WindDirection string
    WindGust string
    Pressure string
    TodaysHigh string
    TodaysLow string
}

type ForecastRow struct {
    Date string
    PrimaryWaveHeight string
    PrimaryPeriod string
    PrimaryDegrees string
    SecondaryWaveHeight string
    SecondaryPeriod string
    SecondaryDegrees string
}

func degreesToCompassLabel(degrees float64) string {
    // Define the 16-wind compass directions
    compassLabels := []string{"N", "NNE", "NE", "ENE", "E", "ESE", "SE", "SSE", "S", "SSW", "SW", "WSW", "W", "WNW", "NW", "NNW"}

    // Normalize degrees to be within the range [0, 360)
    degrees = math.Mod(degrees, 360.0)

    // Calculate the index in the compassLabels slice based on degrees
    index := int(math.Round(degrees/22.5)) % len(compassLabels)

    return compassLabels[index]
}

func parseBullFile(data string) ([]ForecastRow, error) {
    // Parse the data from the bull file
    // For simplicity, I'm just returning the raw data
    forecast := []ForecastRow{}
    lines := strings.Split(data, "\n")

   // dateline := lines[3]
   // parts := strings.Fields(dateline)
   // currentdate:= parts[3]

    lines = lines[6:]

    for _, line := range lines {
        // break if the line starts with "n :"
        if strings.HasPrefix(line, "n :") {
            break
        }
        // remove all "|" and replace with space
        line = strings.ReplaceAll(line, "|", " ")
        line = strings.ReplaceAll(line, "*", "")
        // split by space
        parts := strings.Fields(line)
        if len(parts) < 10 {
            continue
        }

        // Parse the data from the bull file
        forecast = append(forecast, ForecastRow{
            Date: parts[0] + " " + parts[1],
            // strconv returns a float64 and an error, so we need to handle the error
            PrimaryWaveHeight: parts[4],
            PrimaryPeriod: parts[5],
            PrimaryDegrees: parts[6],
            SecondaryWaveHeight: parts[7],
            SecondaryPeriod: parts[8],
            SecondaryDegrees: parts[9],
        })
    }
    return forecast, nil
}

func getPredictions(c *gin.Context) {
    stationId := c.Param("stationId")
    // You might want to add error handling here if stationId is mandatory

    client := resty.New()
    // Adjust the date and time formatting to match your requirements
    now := time.Now().UTC()
    // subtract a day
    now = now.AddDate(0, 0, -1)
    formattedDate := now.Format("20060102")
    // formattedTime := now.Hour() / 6 * 6
    formattedTime := 6
    url := fmt.Sprintf("https://nomads.ncep.noaa.gov/pub/data/nccf/com/gfs/prod/gfs.%s/%02d/wave/station/bulls.t%02dz/gfswave.%s.bull", formattedDate, formattedTime, formattedTime, stationId)
    fmt.Println(url)

    resp, err := client.R().SetHeader("X-Requested-With", "XMLHttpRequest").Get(url)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
    }

    data,err := resp.String(), nil
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
        return
    }

    parsedData, err := parseBullFile(data)

    tmpl, err := template.ParseFiles("templates/forecast.html")

    returndata := map[string]interface{}{"forecast": parsedData}
    // Execute the template with the prediction data
    htmlBuffer := new(bytes.Buffer)
    err = tmpl.Execute(htmlBuffer, returndata)
    if err != nil {
        log.Println(err)
        c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
    }

    c.Header("Content-Type", "text/html; charset=utf-8")
    c.String(http.StatusOK, htmlBuffer.String())
    c.Status(http.StatusOK)
}


func main() {
    //gin.SetMode(gin.ReleaseMode)
    router := gin.Default()

    router.ForwardedByClientIP = true
    //router.SetTrustedProxies([]string{"127.0.0.1","192.168.1.250", "192.168.1.1"})

    // main route
    router.GET("/", func(c *gin.Context) {
        // predictions, err := getPredictions()
        //currentReport, err := getCurrentReport() // we get the current report on the client side
        //localMapData, err := queryLocalMapData()

        // Parse the HTML template
        // tmpl, err := template.ParseFiles("day.html")
        tmpl, err := template.ParseFiles("day.html")
        //send data to template with data := map[string]interface{}{... define map ..}
        // Execute the template with the prediction data
        //htmlBuffer := new(bytes.Buffer)
        //err = tmpl.Execute(htmlBuffer, data)

        // print
        err = tmpl.Execute(c.Writer, nil)
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
            return
        }

        c.Header("Content-Type", "text/html; charset=utf-8")
        //c.String(http.StatusOK, htmlBuffer.String())
        c.Status(http.StatusOK)
    })

    router.GET("/forecast/:stationId", getPredictions) 

    router.Run(":8080")
}
