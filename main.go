package main

import (
    "fmt"
    "net/http"
    "os"
    "log"
)

type Day struct {
    Title string
    Body []byte
}

func handler(w http.ResponseWriter, r *http.Request) {
    // filename := day + ".txt"
    filename := "day.txt"
    body, err := os.ReadFile(filename)
    if err != nil {
        fmt.Fprintf(w, "Error reading file: %s", filename)
        return
    }
    // return an html file to the client
    fmt.Fprintf(w, "<h1>%s</h1><div>%s</div>", filename, body)

}

func main() {
    fmt.Println("Hello, World!")
    http.HandleFunc("/", handler)
    log.Fatal(http.ListenAndServe(":8080", nil))
}
