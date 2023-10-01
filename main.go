
package main

import (
	"database/sql"
	"html/template"
    "fmt"
	"log"
	"net/http"
    "bytes"
//	"path/filepath"

	"github.com/gin-gonic/gin"
    _ "github.com/mattn/go-sqlite3"
)

type Prediction struct {
    Counter int
	WaveHeight float64
	Direction  float64
	Period     float64
}

func main() {
	// Initialize the SQLite3 database connection
	db, err := sql.Open("sqlite3", "../nbdc-buoydata/db.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Initialize the Gin web framework
	router := gin.Default()

	// Define a route to handle requests
	router.GET("/", func(c *gin.Context) {
		// Query the database to get the latest prediction data
        var predictions []Prediction
        for i:= 1; i < 4; i++ {
            var prediction Prediction
            query := fmt.Sprintf("SELECT WvHT, MWD, APD FROM predictions WHERE inDays = %d ORDER BY datetime DESC LIMIT 1", i)
            err := db.QueryRow(query).Scan(&prediction.WaveHeight, &prediction.Direction, &prediction.Period)
            if err != nil {
                log.Println(err)
                c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
                return
            }

            predictions = append(predictions, Prediction{Counter: i, WaveHeight: prediction.WaveHeight, Direction: prediction.Direction, Period: prediction.Period})
            // predictions = append(predictions, prediction)
        }

		// Parse the HTML template
		tmpl, err := template.ParseFiles("day.html")
		if err != nil {
			log.Println(err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
			return
		}

		// Execute the template with the prediction data
		htmlBuffer := new(bytes.Buffer)
		err = tmpl.Execute(htmlBuffer, predictions)
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
	router.Run(":8080")
}
