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
    //fmt.Println(lastupdate)
    t, err := time.Parse("02 01 15:04 2006", lastupdate)
    //fmt.Println(t)

    
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


func queryLocalMapData() ([]MapData, error)  {
    // Open the SQLite database
    db, err := sql.Open("sqlite3", "libsql/local.db")
    defer db.Close()

    rows, err := db.Query("SELECT * FROM wave_predictions")

    if err != nil {
        fmt.Fprintf(os.Stderr, "failed to execute query: %v\n", err)
        os.Exit(1)
    }

    defer rows.Close()

    var mapdata []MapData

    for rows.Next() {
        var point MapData

        err := rows.Scan(&point.Datetime, &point.Latitude, &point.Longitude, &point.Waveheight, &point.Period, &point.Direction, &point.Datapointsaverage)

        if err != nil {
            fmt.Println("Error scanning row:", err)
            return nil, err
        }
        //fmt.Println(point, "\n")

        mapdata = append(mapdata, point)
    }

    if err := rows.Err(); err != nil {
        fmt.Println("Error during rows iteration:", err)
    }
    return mapdata, nil
}

func getPredictions() ([]Prediction, error) {
        // currently, we don't need the dbUrl, but we will need it later in main
        // var dbUrl = "../nbdc-buoydata/db.db"
        API_KEY := os.Getenv("API_KEY")
        API_URL := os.Getenv("API_URL")

        if API_KEY == "" {
            fmt.Println("API_KEY environment variable not set.")
            return nil, fmt.Errorf("API_KEY environment variable not set")
        }

        // var dbUrl = "libsql://database-evancoons22.turso.io?authToken=${envVarValue}"
        var dbUrl = fmt.Sprintf("%s?authToken=%s", API_URL, API_KEY) // linux
        //var dbUrl = fmt.Sprintf(API_URL, API_KEY) // mac ... for some reason

        db, err := sql.Open("libsql", dbUrl)
        if err != nil {
            log.Fatal(err)
        }
        defer db.Close()

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
                return nil, err
            }
            prediction.DirectionLabel = degreesToCompassLabel(prediction.DirectionDegrees)
            //fmt.Println(prediction, "\n")

            //fmt.Println(prediction.Title)
            predictions = append(predictions, prediction)
        }
        return predictions, nil
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

    //router.Run(":8080")
    router.Run()
}


// All functions from fetch.go

//func notmain() {
	// Schedule the data fetching every 6 hours
	//ticker := time.NewTicker(6 * time.Hour)
	// quit := make(chan struct{})
	// go func() {
	//     for {
	//         select {
	//         case <-ticker.C:
	//             fetchDataAndStore()
	//         case <-quit:
	//             ticker.Stop()
	//             return
	//         }
	//     }
	// }()

	// Keep the application running
	// select {}
	//fethDataAndStore()
//}

//func fetchDataAndStore() {
//    // 1. Fetch data from your source
//    data, err := fetchDataFromSource()
//    if err != nil {
//        log.Printf("Error fetching data: %v", err)
//        return
//    }
//
//    // 2. Open SQLite database
//    db, err := sql.Open("sqlite3", "./forecast.db")
//    if err != nil {
//        log.Fatal(err)
//    }
//    defer db.Close()
//
//    // 3. Store data in SQLite
//    err = storeDataInSQLite(db, data)
//    if err != nil {
//        log.Printf("Error storing data in SQLite: %v", err)
//    }
//}

//func fetchDataFromSource() ([]MapData, error) {
//    API_KEY := os.Getenv("API_KEY")
//    API_URL := os.Getenv("API_URL")
//    url:= fmt.Sprintf("%s?authToken=%s", API_URL, API_KEY)
//    db, err := sql.Open("libsql", url)
//
//    if err != nil {
//        fmt.Fprintf(os.Stderr, "failed to open db %s: %s", url, err)
//        os.Exit(1)
//    }
//    defer db.Close()
//
//    rows, err := db.Query("SELECT * FROM wave_predictions")
//    if err != nil {
//        fmt.Fprintf(os.Stderr, "failed to query db: %s", err)
//        os.Exit(1)
//    }
//
//    var data []MapData
//
//    for rows.Next() {
//        var d MapData
//        if err := rows.Scan(&d.datetime, &d.latitude, &d.longitude, &d.waveheight, &d.period, &d.direction, &d.datapointsaverage); err != nil {
//            fmt.Fprintf(os.Stderr, "failed to scan: %s", err)
//            return
//        }
//        data = append(data, d)
//    }
//    if err := rows.Err(); err != nil {
//        fmt.Fprintf(os.Stderr, "error during row iteration", err)
//        return
//    }
//}

// all functions from replica.go

//package main

//import (
//)

//func dontrun() {
//    dbName := "local.db"
//    //primaryUrl := "libsql://[DATABASE].turso.io"
//    //authToken := "..."
//    API_KEY := os.Getenv("API_KEY")
//    API_URL := os.Getenv("API_URL")
//
//    //dir, err := os.MkdirTemp("", "libsql-*")
//    // make a normal directory
//    dir := "libsql"
//    err := os.Mkdir(dir, 0750)
//
//    if err != nil {
//        fmt.Println("error creating directory", err)
//        os.Exit(1)
//    }
//
//
//    dbPath := filepath.Join(dir, dbName)
//
//    connector, err := libsql.NewEmbeddedReplicaConnector(dbPath, API_URL, API_KEY)
//    if err != nil {
//        fmt.Println("Error creating connector:", err)
//        os.Exit(1)
//    }
//    defer connector.Close()
//
//    db := sql.OpenDB(connector)
//    defer db.Close()
//}
//
