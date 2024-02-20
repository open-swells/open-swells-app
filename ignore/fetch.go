package main

import (
    "database/sql"
    "log"
    "time"
    _ "github.com/mattn/go-sqlite3"
    _ "github.com/tursodatabase/libsql-client-go/libsql"
)

type MapData struct { 
    datetime string
    latitude float64
    longitude float64
    waveheight float64
    period float64
    direction float64
    datapointsaverage float64
}

func main() {
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
   fethDataAndStore()
}

func fetchDataAndStore() {
    // 1. Fetch data from your source
    data, err := fetchDataFromSource()
    if err != nil {
        log.Printf("Error fetching data: %v", err)
        return
    }

    // 2. Open SQLite database
    db, err := sql.Open("sqlite3", "./forecast.db")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    // 3. Store data in SQLite
    err = storeDataInSQLite(db, data)
    if err != nil {
        log.Printf("Error storing data in SQLite: %v", err)
    }
}

func fetchDataFromSource() ([]MapData, error) {
    API_KEY := os.Getenv("API_KEY")
    API_URL := os.Getenv("API_URL")
    url:= fmt.Sprintf("%s?authToken=%s", API_URL, API_KEY)
    db, err := sql.Open("libsql", url)

    if err != nil {
        fmt.Fprintf(os.Stderr, "failed to open db %s: %s", url, err)
        os.Exit(1)
    }
    defer db.Close()

    rows, err := db.Query("SELECT * FROM wave_predictions")
    if err != nil {
        fmt.Fprintf(os.Stderr, "failed to query db: %s", err)
        os.Exit(1)
    }

    var data []MapData

    for rows.Next() {
        var d MapData
        if err := rows.Scan(&d.datetime, &d.latitude, &d.longitude, &d.waveheight, &d.period, &d.direction, &d.datapointsaverage); err != nil {
            fmt.Fprintf(os.Stderr, "failed to scan: %s", err)
            return
        }
        data = append(data, d)
    }
    if err := rows.Err(); err != nil {
        fmt.Fprintf(os.Stderr, "error during row iteration", err)
        return
    }
}

func storeDataInSQLite(db *sql.DB, data []YourDataType) error {
    // Implement the logic to store fetched data in the SQLite database
    // Placeholder return
    return nil
}

