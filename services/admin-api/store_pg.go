package main

// store_pg.go — Postgres-backed persistence for admin-plane users (A6).
//
// When DATABASE_URL is set, admin users are persisted in Postgres (table
// admin_users, JSONB document per row, email primary key). On boot the
// store is hydrated FROM Postgres; an empty table is seeded from the dev
// seed set. All user mutations write through. The rest of the admin-plane
// state (tenants, flags, gates...) stays in-memory for now — see the TODO
// below; users were the audit-critical piece (plaintext + in-mem).
//
// TODO: persist tenants/flags/api-keys the same way behind a small
// interface so the console plane survives restarts fully.

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

type pgUsers struct {
	conn *pgx.Conn
}

// openPgUsers connects and ensures the schema. Returns (nil, nil) when
// DATABASE_URL is unset (dev in-mem mode).
func openPgUsers() (*pgUsers, error) {
	url := databaseURL()
	if url == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return nil, err
	}
	_, err = conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS admin_users (
			email TEXT PRIMARY KEY,
			doc   JSONB NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	if err != nil {
		conn.Close(context.Background())
		return nil, err
	}
	return &pgUsers{conn: conn}, nil
}

func databaseURL() string {
	return envOr("DATABASE_URL", "")
}

// hydrate loads users from Postgres into s. If the table is empty the
// current (seeded) users are written up instead. Returns whether Postgres
// backing is active.
func (p *pgUsers) hydrate(s *Store) (bool, error) {
	if p == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := p.conn.Query(ctx, "SELECT doc FROM admin_users")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	loaded := map[string]*User{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var u User
		if err := json.Unmarshal(raw, &u); err != nil {
			continue
		}
		loaded[u.Email] = &u
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(loaded) == 0 {
		// first boot against an empty table: persist the seed set
		for _, u := range s.Users {
			if err := p.upsert(u); err != nil {
				return false, err
			}
		}
		log.Printf("component=admin-api postgres: seeded %d users into admin_users", len(s.Users))
		return true, nil
	}
	s.Users = loaded
	log.Printf("component=admin-api postgres: loaded %d users from admin_users", len(loaded))
	return true, nil
}

func (p *pgUsers) upsert(u *User) error {
	if p == nil {
		return nil
	}
	raw, err := json.Marshal(u)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = p.conn.Exec(ctx, `
		INSERT INTO admin_users (email, doc, updated_at) VALUES ($1, $2, now())
		ON CONFLICT (email) DO UPDATE SET doc = $2, updated_at = now()`,
		u.Email, raw)
	return err
}

// upsertUserPg writes a user through to Postgres when configured (no-op in
// dev in-mem mode). Errors are logged, not fatal — the console stays
// available in graceful degradation, and the next boot re-hydrates.
func (a *app) upsertUserPg(u *User) {
	if a.pg == nil {
		return
	}
	if err := a.pg.upsert(u); err != nil {
		log.Printf("component=admin-api postgres upsert %s: %v", u.Email, err)
	}
}
