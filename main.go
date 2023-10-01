
package main

import (
	"database/sql"
	"html/template"
	"log"
	"net/http"
    "bytes"
//	"path/filepath"

	"github.com/gin-gonic/gin"
    _ "github.com/mattn/go-sqlite3"
)

type Prediction struct {
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
		var prediction Prediction
		err := db.QueryRow("SELECT WvHT, MWD, APD FROM predictions ORDER BY datetime DESC LIMIT 1").Scan(&prediction.WaveHeight, &prediction.Direction, &prediction.Period)
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

		// Execute the template with the prediction data
		htmlBuffer := new(bytes.Buffer)
		err = tmpl.Execute(htmlBuffer, prediction)
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
