package app

import "database/sql"

func InitDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
 PRAGMA foreign_keys = ON;
 PRAGMA busy_timeout = 5000;
		CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			login TEXT UNIQUE NOT NULL,
			password TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS expressions (
			id TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL,
			expression TEXT NOT NULL,
			status TEXT NOT NULL,
			result REAL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(user_id) REFERENCES users(id)
		);
	`)
	if err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}
