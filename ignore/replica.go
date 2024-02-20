package main

import (
  "database/sql"
  "fmt"
  "os"
  "path/filepath"
  "github.com/libsql/go-libsql"
)

func main() {
    dbName := "local.db"
    //primaryUrl := "libsql://[DATABASE].turso.io"
    //authToken := "..."
    API_KEY := os.Getenv("API_KEY")
    API_URL := os.Getenv("API_URL")

    //dir, err := os.MkdirTemp("", "libsql-*")
    // make a normal directory
    dir := "libsql"
    err := os.Mkdir(dir, 0750)

    if err != nil {
        fmt.Println("error creating directory", err)
        os.Exit(1)
    }


    dbPath := filepath.Join(dir, dbName)

    connector, err := libsql.NewEmbeddedReplicaConnector(dbPath, API_URL, API_KEY)
    if err != nil {
        fmt.Println("Error creating connector:", err)
        os.Exit(1)
    }
    defer connector.Close()

    db := sql.OpenDB(connector)
    defer db.Close()
}

