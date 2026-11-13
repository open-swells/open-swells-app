package main

// On-disk layout shared by the app, the forecast pipeline and dev machines
// (see "Data and config layout" in the top-level README). Config and data
// live outside the checkout, at the same paths on every machine:
//
//	$OPENSWELLS_CONFIG_DIR (/etc/open-swells)
//	    open-swells-app.env   runtime config, loaded at startup
//	    firebase.json         Firebase service account key (optional)
//	$OPENSWELLS_DATA_DIR (/var/lib/open-swells)
//	    main.db               SQLite: users, favorites, stations, surf zones
//	    forecast/             published by the pipeline, served under /static/
//	    spot_forecast_history.json.gz
//
// DB_PATH, FORECAST_DIR and FIREBASE_CREDENTIALS still override single paths.

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	defaultConfigDir = "/etc/open-swells"
	defaultDataDir   = "/var/lib/open-swells"
	appEnvFile       = "open-swells-app.env"
	firebaseKeyFile  = "firebase.json"
)

func configDir() string { return getenvDefault("OPENSWELLS_CONFIG_DIR", defaultConfigDir) }

func dataDir() string { return getenvDefault("OPENSWELLS_DATA_DIR", defaultDataDir) }

// firebaseCredentialsPath is FIREBASE_CREDENTIALS, else the config
// directory's firebase.json when it exists, else "" (sign-in disabled).
func firebaseCredentialsPath() string {
	if v := os.Getenv("FIREBASE_CREDENTIALS"); v != "" {
		return v
	}
	path := filepath.Join(configDir(), firebaseKeyFile)
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

// requireWritableDir explains how to create the data layout instead of
// letting SQLite fail with "unable to open database file".
func requireWritableDir(dir string) error {
	info, err := os.Stat(dir)
	if err == nil && !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if err == nil {
		probe, perr := os.CreateTemp(dir, ".write-check-*")
		if perr == nil {
			probe.Close()
			os.Remove(probe.Name())
			return nil
		}
		err = perr
	}
	return fmt.Errorf("data directory %s is not usable (%v); create it once with `sudo ops/setup-dirs.sh` from the repository root, or set OPENSWELLS_DATA_DIR to a directory you own", dir, err)
}

// legacyDBGuard refuses to start when the app would create a new, empty
// database while one from the old layout (./main.db beside the binary, the
// pre-/var/lib default) still exists: that would hide every user's
// favorites behind a fresh file. Moving the old file (see
// ops/migrate-legacy-layout.sh) or setting DB_PATH resolves it.
func legacyDBGuard(dbPath string, dbPathSet bool) error {
	if dbPathSet {
		return nil
	}
	if _, err := os.Stat(dbPath); err == nil {
		return nil
	}
	legacy, err := filepath.Abs("main.db")
	if err != nil {
		return nil
	}
	if _, err := os.Stat(legacy); err != nil {
		return nil
	}
	return fmt.Errorf("refusing to create a new database at %s while %s exists (the old default location); copy it there with ops/migrate-legacy-layout.sh, or set DB_PATH=%s to keep using it", dbPath, legacy, legacy)
}
