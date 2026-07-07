// File: examples/dbx-http/server/main.go
// Purpose: Provides runnable KPro database example code.

package main

import (
	"context"
	"flag"
	"log"

	"github.com/saramand9/bignet/kpro/database/dbx"
	routeapp "github.com/saramand9/bignet/kpro/examples/dbx-http/server/internal/app"
	"github.com/saramand9/bignet/kpro/server"
	_ "modernc.org/sqlite"
)

func main() {
	addr := flag.String("addr", ":8080", "http listen address")
	udpAddr := flag.String("udp", ":8089", "udp listen address, disabled when empty")
	grpcAddr := flag.String("grpc", ":9090", "grpc listen address, disabled when empty")
	dsn := flag.String("dsn", "file:kpro-dbx-example.db", "sqlite data source name")
	flag.Parse()

	ctx := context.Background()
	database, err := dbx.Open(ctx, dbx.Config{
		DriverName:   "sqlite",
		DSN:          *dsn,
		Dialect:      dbx.SQLite,
		MaxOpenConns: 1,
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
	if err := routeapp.RegisterRoutes(ctx, app, database, routeapp.Config{HTTPAddr: *addr, UDPAddr: *udpAddr, GRPCAddr: *grpcAddr}); err != nil {
		log.Fatal(err)
	}

	log.Printf("dbx example server listening http=%s udp=%s grpc=%s", *addr, *udpAddr, *grpcAddr)
	log.Fatal(app.ListenAndServe())
}
