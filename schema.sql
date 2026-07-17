-- Reference schema for main.db. The app applies these statements itself as
-- startup migrations (openDatabase in main.go), so a missing db file is
-- created automatically; this file is for rebuilding by hand:
--   sqlite3 main.db < schema.sql

-- user table
CREATE TABLE IF NOT EXISTS users (
    uid TEXT PRIMARY KEY
);

-- (many-to-many user ids to buoy ids)
CREATE TABLE IF NOT EXISTS user_buoys (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    uid TEXT NOT NULL,
    buoy_id TEXT NOT NULL,
    FOREIGN KEY (uid) REFERENCES users (uid) ON DELETE CASCADE
);

-- one favorite per (user, buoy); also serves as the lookup index for
-- "favorites for user". The app applies this at startup too.
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_buoys_uid_buoy ON user_buoys (uid, buoy_id);

-- (many-to-many user ids to favorite surf spot ids from beaches.json)
CREATE TABLE IF NOT EXISTS user_spots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    uid TEXT NOT NULL,
    spot_id TEXT NOT NULL,
    FOREIGN KEY (uid) REFERENCES users (uid) ON DELETE CASCADE
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_spots_uid_spot ON user_spots (uid, spot_id);


-- Surf zone geometry from api.weather.gov (see surfzone.go): marker anchor
-- (centroid) plus the boundary GeoJSON, fetched once per zone.
CREATE TABLE IF NOT EXISTS surf_zones (
    zone_id TEXT PRIMARY KEY,
    latitude REAL NOT NULL,
    longitude REAL NOT NULL,
    geometry TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
