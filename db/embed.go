// Package db embeds the SQL that defines our database: the goose migrations
// and the script that creates the app roles. internal/db/dbtest uses it now;
// the migration Job uses it from feature #17. Import it as schema, since
// internal/db is also named db.
package db

import "embed"

// Migrations holds migrations/*.sql; pass fs.Sub(Migrations, "migrations") to goose.
//
//go:embed migrations/*.sql
var Migrations embed.FS

// Roles is db/roles.sql: run it as a superuser before migrating.
//
//go:embed roles.sql
var Roles string
