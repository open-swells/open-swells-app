package main

import (
    "os"
    "strconv"
    "strings"
    "bufio"
    "time"
	"database/sql"
	"html/template"
    "fmt"
	"log"
	"net/http"
	"github.com/gin-gonic/gin"
    _ "github.com/mattn/go-sqlite3"
    _ "github.com/libsql/libsql-client-go/libsql"
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

func degreesToCompassLabel(degrees float64) string {
    // Define the 16-wind compass directions
    compassLabels := []string{"N", "NNE", "NE", "ENE", "E", "ESE", "SE", "SSE", "S", "SSW", "SW", "WSW", "W", "WNW", "NW", "NNW"}

    // Normalize degrees to be within the range [0, 360)
    degrees = math.Mod(degrees, 360.0)

    // Calculate the index in the compassLabels slice based on degrees
    index := int(math.Round(degrees/22.5)) % len(compassLabels)

    return compassLabels[index]
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

    router.GET("/forecast/:stationId", func(c *gin.Context) {
        date := time.Now().Format("20060102")
        fmt.Println("Date: %v", date)
        // print a message
        fmt.Println("Fetching forecast for stationId: ", c.Param("stationId"))
        // get the hour of the day
        // cast the hour to an int, divide by 6, then multiply by 6 to get the nearest 6 hour interval
        hour, err := strconv.Atoi(time.Now().Format("15"))
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
            return
        }
        hour = (hour / 6) * 6
        // convert the hour back to a string

        url := "https://corsproxy.io/?https://nomads.ncep.noaa.gov/pub/data/nccf/com/gfs/prod/gfs." + date + "/" + strconv.Itoa(hour)  + "/wave/station/bulls.t18z/gfswave." + c.Param("stationId") + ".bull"

        // get request
        resp, err := http.Get(url)
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
            return
        }
        defer resp.Body.Close()
        fmt.Sprintf("Response: %v", resp)

        // return this repsonse to the client

    })

    router.Run()
}
