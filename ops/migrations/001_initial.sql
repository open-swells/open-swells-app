-- Reference schema for main.db (schema version 1, tracked via
-- PRAGMA user_version). The app applies this itself at startup
-- (openDatabase in server.go), so a missing db file is created
-- automatically; this file is for rebuilding by hand:
--   sqlite3 main.db < ops/migrations/001_initial.sql
--
-- Conventions:
--   * All timestamps are ISO-8601 UTC strings (SQLite datetime('now')).
--   * User-owned rows key on the Firebase uid and cascade on user delete.
--   * Favorites use natural composite keys: the pair IS the row, so the
--     primary key doubles as the "favorites for user" index and duplicate
--     favorites are impossible by construction.

-- Registered users, keyed by Firebase auth uid. Identity details (email,
-- display name) live with the auth provider, not here.
CREATE TABLE IF NOT EXISTS users (
    uid        TEXT PRIMARY KEY,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
) WITHOUT ROWID;

-- Favorite NDBC stations. station_id matches buoys.station_id but is not a
-- foreign key on purpose: the buoys registry is refreshed from NDBC and a
-- favorite must survive its station temporarily dropping out of the feed.
CREATE TABLE IF NOT EXISTS user_buoys (
    uid        TEXT NOT NULL REFERENCES users (uid) ON DELETE CASCADE,
    station_id TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (uid, station_id)
) WITHOUT ROWID;

-- Favorite surf spots. spot_id references data/spots.json (static per
-- deploy), so there is no table to foreign-key against.
CREATE TABLE IF NOT EXISTS user_spots (
    uid        TEXT NOT NULL REFERENCES users (uid) ON DELETE CASCADE,
    spot_id    TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (uid, spot_id)
) WITHOUT ROWID;

-- Station registry, refreshed daily from NDBC (see stations.go). Not user
-- data: rebuildable from the feed, but inactive rows are kept so favorites
-- of stations that stopped reporting still resolve to a name.
CREATE TABLE IF NOT EXISTS buoys (
    station_id TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    latitude   REAL NOT NULL,
    longitude  REAL NOT NULL,
    active     INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    last_seen  TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- Cache of surf zone geometry from api.weather.gov (see surfzone.go):
-- marker anchor (centroid) plus the boundary GeoJSON, fetched once per
-- zone. Rebuildable: safe to truncate, the app refetches on demand.
CREATE TABLE IF NOT EXISTS surf_zones (
    zone_id    TEXT PRIMARY KEY,
    latitude   REAL NOT NULL,
    longitude  REAL NOT NULL,
    geometry   TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

PRAGMA user_version = 1;
