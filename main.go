
package main

import (
    //"regexp"
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
    "bytes"
//	"path/filepath"

    // "github.com/gin-contrib/secure"
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


func getCurrentReport() (ValuesStruct, error) {
    // Make an HTTP GET request to fetch data from the provided link
    resp, err := http.Get("https://www.ndbc.noaa.gov/data/realtime2/46221.spec")
    if err != nil {
        return ValuesStruct{}, err
    }
    defer resp.Body.Close()

    // Initialize variables to store labels and values
    //var labels []string
    var values []string

    // Read the response line by line
    scanner := bufio.NewScanner(resp.Body)
    rowCounter := 0
    for scanner.Scan() {
        line := scanner.Text()

        // Split the first row by spaces to store labels
        if rowCounter == 0 {
            //labels = strings.Fields(line)
            //skip the first row
        } else if rowCounter == 1 {
            // Skip the second row
        } else if rowCounter == 2 {
            // Split the third row by spaces to store values
            values = strings.Fields(line)
            break // Stop reading after finding the values
        }

        rowCounter++
    }

    if err := scanner.Err(); err != nil {
        return ValuesStruct{}, err
    }
    //remove first 5 values of labels and first 4 values of values and replace with current date 

    //create variable lastupdate from the first 4 values of values. 
    lastupdate := values[2] + " " + values[1] + " " + values[3] + ":" + values[4] + " " + values[0]
    fmt.Println(lastupdate)
    t, err := time.Parse("02 01 15:04 2006", lastupdate)
    fmt.Println(t)

    
    //labels = append(labels[:0], labels[5:]...)
    values = append(values[:0], values[5:]...)
    // add a field "Date" in labels, and add current date in values
    //labels = append(labels, "Date")
    currentDate := time.Now()
    values = append(values, currentDate.Format("Mon, Jan 2 15:04"))
    // put values into type valuesStruct and return
    //fmt.Println(values[10])

    var valuesStruct ValuesStruct
    valuesStruct.WvHT, _ = strconv.ParseFloat(values[0], 64)
    valuesStruct.SwH, _ = strconv.ParseFloat(values[1], 64)
    valuesStruct.SwP, _ = strconv.ParseFloat(values[2], 64)
    valuesStruct.WWH, _ = strconv.ParseFloat(values[3], 64)
    valuesStruct.WWP, _ = strconv.ParseFloat(values[4], 64)
    valuesStruct.SwD = values[5]
    valuesStruct.WWD = values[6]
    valuesStruct.STEEPNESS = values[7]
    valuesStruct.APD, _ = strconv.ParseFloat(values[8], 64)
    valuesStruct.MWD, _ = strconv.ParseFloat(values[9], 64)
    valuesStruct.Date = values[10]
    valuesStruct.UpdateDate = t.Format("Mon, Jan 2 15:04")

    //return CurrentReport{Labels: labels, Values: valuesStruct}, nil
    return valuesStruct, nil
}


func main() {
	// Initialize the SQLite3 database connection

    // var dbUrl = "../nbdc-buoydata/db.db"
    API_KEY := os.Getenv("API_KEY")
    API_URL := os.Getenv("API_URL")

    if API_KEY == "" {
        fmt.Println("API_KEY environment variable not set.")
        return
    }

    // var dbUrl = "libsql://database-evancoons22.turso.io?authToken=${envVarValue}"
    var dbUrl = fmt.Sprintf(API_URL, API_KEY)

	//db, err := sql.Open("sqlite3", dbUrl)
	db, err := sql.Open("libsql", dbUrl)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

    gin.SetMode(gin.ReleaseMode)
	// Initialize the Gin web framework
	router := gin.Default()

    router.ForwardedByClientIP = true
    router.SetTrustedProxies([]string{"127.0.0.1","192.168.1.250", "192.168.1.1"})

	// Define a route to handle requests
	router.GET("/", func(c *gin.Context) {
		// Query the database to get the latest prediction data
        var predictions []Prediction
        currentDate := time.Now()

        for i:= 1; i < 4; i++ {

            predictionDate := currentDate.AddDate(0, 0, i)

            // Format the title for the prediction (e.g., "Mon, Oct 2")
            title := predictionDate.Format("Mon, Jan 2")
            //title = "test"
            var prediction Prediction
            prediction.Title = title

            query := fmt.Sprintf("SELECT WvHT, MWD, APD FROM predictions WHERE inDays = %d ORDER BY datetime DESC LIMIT 1", i)
            err := db.QueryRow(query).Scan(&prediction.WaveHeight, &prediction.DirectionDegrees, &prediction.Period)
            if err != nil {
                log.Println(err)
                c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
                return
            }
            prediction.DirectionLabel = degreesToCompassLabel(prediction.DirectionDegrees)

            //fmt.Println(prediction.Title)
            predictions = append(predictions, prediction)
        }

        // get the current report
        currentReport, err := getCurrentReport()
        fmt.Println(currentReport)
        if err != nil {
            log.Println(err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
            return
        }

		// Parse the HTML template
		tmpl, err := template.ParseFiles("day.html")
		if err != nil {
			log.Println(err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
			return
		}

        // data to send to template
        data := map[string]interface{}{
            "Predictions":  predictions,
            "CurrentReport": currentReport,
        }

		// Execute the template with the prediction data
		htmlBuffer := new(bytes.Buffer)
		err = tmpl.Execute(htmlBuffer, data)
		if err != nil {
			log.Println(err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
			return
		}

		// Serve the HTML response
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, htmlBuffer.String())
	})

    // Start the web server
    // router.Run("127.0.0.1:8080")
    //router.Run(":8080")
    router.Run()
}
