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
2. `$ FIREBASE_CREDENTIALS=/path/to/service-account-key.json go run ./server`
3. go to localhost:8081

Configuration (all optional except Firebase credentials):

| Env var | Default | Purpose |
| --- | --- | --- |
| `FIREBASE_CREDENTIALS` | (uses Application Default Credentials) | Path to the Firebase service account key |
| `PORT` | `8081` | Listen port |
| `DB_PATH` | `./main.db` | SQLite database path (created automatically; see `ops/migrations/001_initial.sql`) |
| `FORECAST_DIR` | `./data/forecast` | Generated forecast GeoJSON, PNG, tide, and metadata files |
| `SPOTS_PATH` | `./data/spots.json` | Tracked surf-spot reference data |
| `TEMPLATE_DIR` | `./web/templates` | Go HTML page and component templates |
| `TRUSTED_PROXIES` | `127.0.0.1,::1` | Comma-separated reverse proxy IPs |
| `GIN_MODE` | `release` | Set `debug` for verbose gin output |
| `RATE_LIMIT_RPM` / `RATE_LIMIT_BURST` | `120` / `30` | Per-client limit for dynamic pages and APIs |
| `STATIC_RATE_LIMIT_RPM` / `STATIC_RATE_LIMIT_BURST` | `600` / `100` | Per-client limit for generated static data |
| `EXPENSIVE_RATE_LIMIT_RPM` / `EXPENSIVE_RATE_LIMIT_BURST` | `12` / `4` | Per-client limit for forecast and upstream-backed routes |
| `SUMMARY_RATE_LIMIT_RPM` / `SUMMARY_RATE_LIMIT_BURST` | `6` / `2` | Per-client limit for forecast-summary generation |
| `USER_RATE_LIMIT_RPM` / `USER_RATE_LIMIT_BURST` | `30` / `10` | Per-user limit for authenticated routes |
| `RATE_LIMIT_MAX_CLIENTS` | `10000` | Maximum in-memory client buckets |
| `EXPENSIVE_MAX_CONCURRENT` | `16` | Maximum simultaneous upstream/forecast requests |
| `SUMMARY_MAX_CONCURRENT` | `4` | Maximum simultaneous forecast-summary builds |

`GET /healthz` returns 200 when the database is reachable and the contour
data is fresh (<24h), 503 otherwise — point uptime monitoring at it.

The ignored `data/forecast/` directory contains generated forecast products.
For local development it is populated by the sibling `grib-parse-collect`
repository. In production, a separate forecast host delivers those files to
this directory over rsync. They remain available to the browser under the
existing `/static/` URL path. Existing deployments may temporarily use
`STATIC_DIR` as a fallback for `FORECAST_DIR`.

Per-user endpoints (`/api/favorites`, `/forecast-summary`) require a
Firebase ID token in the `Authorization: Bearer` header; the uid is always
derived from the verified token.

Google sign-in uses the current site origin as Firebase's `authDomain`. The
app transparently proxies `/__/auth/` to the project's Firebase Hosting auth
helper, which keeps mobile redirect state first-party in browsers that block
third-party storage. Before deploying a new hostname:

1. Add the exact hostname under Firebase Console → Authentication → Settings
   → Authorized domains.
2. Add `https://YOUR_DOMAIN/__/auth/handler` as an authorized redirect URI for
   the web client's Google OAuth credentials in Google Cloud Console.
3. Keep `/__/auth/` routed to this Go application in the reverse proxy; do not
   redirect or rewrite that path.

### Deploying to Linux

`ops/deploy.sh` deploys the app to `/opt/open-swells-app` over SSH, builds it on
the server, and restarts the existing `open-swells-app` systemd service. The
tracked `ops/systemd/open-swells-app.service` file is provided for initial
server setup;
routine deployments do not install or modify the service definition.

One-time server setup (run as root):

```sh
useradd --system --home-dir /var/lib/open-swells --shell /usr/sbin/nologin openswells
install -d -o root -g openswells -m 0750 /etc/open-swells
install -d -o openswells -g openswells -m 0750 /var/lib/open-swells /var/lib/open-swells/forecast
install -o root -g openswells -m 0640 /path/to/firebase.json /etc/open-swells/firebase.json
install -o root -g root -m 0644 ops/systemd/open-swells-app.service /etc/systemd/system/open-swells-app.service
```

Create `/etc/open-swells/open-swells-app.env` with mode `0640`, owned by
`root:openswells`:

```sh
PORT=8081
DB_PATH=/var/lib/open-swells/main.db
FORECAST_DIR=/var/lib/open-swells/forecast
FIREBASE_CREDENTIALS=/etc/open-swells/firebase.json
TRUSTED_PROXIES=127.0.0.1,::1
```

Then run `systemctl daemon-reload` and deploy. The forecast producer should
write to `/var/lib/open-swells/forecast`; the application account needs write
access there because it creates cached `.gz` siblings.

Add the server IP to `.env`:

```sh
SERVER_IP=203.0.113.10
```

Then run:

```sh
./ops/deploy.sh
```

Optional overrides:

```sh
DEPLOY_USER=root DEPLOY_DIR=/opt/open-swells-app APP_NAME=open-swells-app APP_USER=openswells ./ops/deploy.sh
```

The server must have Go installed and must build with Go 1.26.5 or newer (the
module enforces this; Go's automatic toolchain download may need outbound
access). Server configuration, Firebase credentials, SQLite state, and
forecast data live outside the deploy directory and remain unchanged.

### Resources and Tools
- [htmx](https://htmx.org/)
- [tailwindcss](https://tailwindcss.com/)
- [golang](https://golang.org/)
- [gin-gonic](https://github.com/gin-gonic/gin)
- [leafletjs](https://leafletjs.com/) (maps)
- [d3js](https://d3js.org/) (charts)
- [EMC Operational Wave Models](https://polar.ncep.noaa.gov/waves/index.php)
