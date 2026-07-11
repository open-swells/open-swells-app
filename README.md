<!-- <p align="center">
  <img src="https://github.com/evancoons22/go-mb-surf/blob/main/surf4.png?raw=true" width="300"/>
</p>
-->
## OpenSwells;

### Overview

**[OpenSwells](https://go-surf-app-438594f906bc.herokuapp.com/)** is a free, open-source **surf report** and **16 day swell forecast from 170+ locations**. Reports and forecasts are from the [The Environmental Modeling Center Operational Wave Models](https://polar.ncep.noaa.gov/waves/index.php).
New forecasts are received every 6 hours. 

### Previously

This project was previously a 3 day forecast for my local beach using an LSTM network, and later graph neural networks ([site](https://go-ml-surf-forecast.onrender.com/), [github](https://github.com/evancoons22/nbdc-buoy-data)). I have since expanded it to include a 16 days forecast for 170+ locations thanks to Environmental Modeling Center and NOAA.

### Running the App

You can run this app locally.

1. clone repo
2. `$ FIREBASE_CREDENTIALS=/path/to/service-account-key.json go run .`
3. go to localhost:8081

Configuration (all optional except Firebase credentials):

| Env var | Default | Purpose |
| --- | --- | --- |
| `FIREBASE_CREDENTIALS` | (uses Application Default Credentials) | Path to the Firebase service account key |
| `PORT` | `8081` | Listen port |
| `DB_PATH` | `./main.db` | SQLite database path (created automatically; see `schema.sql`) |
| `STATIC_DIR` | `./static` | Directory with contour geojson + metadata.json |
| `TRUSTED_PROXIES` | `127.0.0.1,::1` | Comma-separated reverse proxy IPs |
| `GIN_MODE` | `release` | Set `debug` for verbose gin output |

`GET /healthz` returns 200 when the database is reachable and the contour
data is fresh (<24h), 503 otherwise — point uptime monitoring at it.

Per-user endpoints (`/api/favorites`, `/forecast-summary`) require a
Firebase ID token in the `Authorization: Bearer` header; the uid is always
derived from the verified token.

### Deploying to Linux

`deploy.sh` deploys the app to `/root/open-swells-app` over SSH, builds it on
the server, and restarts the existing `open-swells-app` systemd service. The
tracked `open-swells-app.service` file is provided for initial server setup;
routine deployments do not install or modify the service definition.

Add the server IP to `.env`:

```sh
SERVER_IP=203.0.113.10
```

Then run:

```sh
./deploy.sh
```

Optional overrides:

```sh
DEPLOY_USER=root DEPLOY_DIR=/root/open-swells-app APP_NAME=open-swells-app ./deploy.sh
```

The server must have Go installed. The server's `.env`, Firebase credentials,
and SQLite database files are excluded from deployment syncs and remain
unchanged.

### Resources and Tools
- [htmx](https://htmx.org/)
- [tailwindcss](https://tailwindcss.com/)
- [golang](https://golang.org/)
- [gin-gonic](https://github.com/gin-gonic/gin)
- [leafletjs](https://leafletjs.com/) (maps)
- [d3js](https://d3js.org/) (charts)
- [EMC Operational Wave Models](https://polar.ncep.noaa.gov/waves/index.php)
