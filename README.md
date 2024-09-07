<!-- <p align="center">
  <img src="https://github.com/evancoons22/go-mb-surf/blob/main/surf4.png?raw=true" width="300"/>
</p>
-->
## OpenSwells;

### [openswells.com](https://openswells.com)
### Overview

**[OpenSwells](https://go-surf-app-438594f906bc.herokuapp.com/)** is a free, open-source **surf report** and **16 day swell forecast from 170+ locations**. Reports and forecasts are from the [The Environmental Modeling Center Operational Wave Models](https://polar.ncep.noaa.gov/waves/index.php).
New forecasts are received every 6 hours. 

### Previously

This project was previously a 3 day forecast for my local beach using an LSTM network, and later graph neural networks ([site](https://go-ml-surf-forecast.onrender.com/), [github](https://github.com/evancoons22/nbdc-buoy-data)). I have since expanded it to include a 16 days forecast for 170+ locations thanks to Environmental Modeling Center and NOAA.

### Running the App

You can run this app locally.

1. clone repo
2. `$ go run .`
3. go to localhost:8081

### Resources and Tools
- [htmx](https://htmx.org/)
- [tailwindcss](https://tailwindcss.com/)
- [golang](https://golang.org/)
- [gin-gonic](https://github.com/gin-gonic/gin)
- [leafletjs](https://leafletjs.com/) (maps)
- [d3js](https://d3js.org/) (charts)
- [EMC Operational Wave Models](https://polar.ncep.noaa.gov/waves/index.php)

### to do
- [X] fix dark mode
- [X] button redesign top right
- [X] new flex layout map and report. 
- [X] remove auto dark mode
    - [] Map should become a template and returned from go
- [] map in forecast and 
- [] eventually, use htmx to replace the report on request (see "show fast report" commented out in button)
