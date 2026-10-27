package main

// Embedded zone data, so viewer time zones resolve even on hosts without
// a system tz database.
import _ "time/tzdata"

func main() {
	run()
}
