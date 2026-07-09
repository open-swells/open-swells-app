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

