// File: examples/dbx-http/server/main.go
// Purpose: Provides runnable KPro database example code.

package main

import (
	"context"
	"flag"
	"log"

	"github.com/saramand9/bignet/kpro/dbx"
	routeapp "github.com/saramand9/bignet/kpro/examples/dbx-http/server/internal/app"
	"github.com/saramand9/bignet/kpro/server"
	"gorm.io/driver/sqlite"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dsn := flag.String("dsn", "file:kpro-dbx-example.db", "sqlite data source name")
	flag.Parse()

	ctx := context.Background()
	database, err := dbx.Open(ctx, dbx.Config{
		GORMDialector: sqlite.Open(*dsn),
		Dialect:       dbx.SQLite,
		MaxOpenConns:  1,
		Async: dbx.AsyncConfig{
			Workers:   2,
			QueueSize: 128,
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	app := server.New(server.Config{Name: "dbx-http-example"})
	app.Manage(database)
	if err := routeapp.RegisterRoutes(ctx, app, database, routeapp.Config{HTTPAddr: *addr}); err != nil {
		log.Fatal(err)
	}

	log.Printf("dbx http example server listening on http://127.0.0.1%s", *addr)
	log.Fatal(app.ListenAndServe())
}
