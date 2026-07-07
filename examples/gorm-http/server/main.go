// File: examples/gorm-http/server/main.go
// Purpose: Provides runnable KPro GORM database example code.

package main

import (
	"context"
	"flag"
	"log"

	gormstore "github.com/saramand9/bignet/kpro/database/relational/gorm"
	routeapp "github.com/saramand9/bignet/kpro/examples/gorm-http/server/internal/app"
	"github.com/saramand9/bignet/kpro/server"
	"gorm.io/driver/sqlite"

	_ "modernc.org/sqlite"
)

func main() {
	addr := flag.String("addr", ":8080", "http listen address")
	udpAddr := flag.String("udp", ":8089", "udp listen address, disabled when empty")
	grpcAddr := flag.String("grpc", ":9090", "grpc listen address, disabled when empty")
	dsn := flag.String("dsn", "file:kpro-gorm-example.db", "sqlite data source name")
	flag.Parse()

	ctx := context.Background()
	database, err := gormstore.Open(ctx, gormstore.Config{
		Name:         "main",
		Dialector:    sqlite.Dialector{DriverName: "sqlite", DSN: *dsn},
		MaxOpenConns: 1,
	})
	if err != nil {
		log.Fatal(err)
	}

	app := server.New(server.Config{Name: "gorm-http-example"})
	app.Manage(database)
	if err := routeapp.RegisterRoutes(ctx, app, database, routeapp.Config{HTTPAddr: *addr, UDPAddr: *udpAddr, GRPCAddr: *grpcAddr}); err != nil {
		log.Fatal(err)
	}

	log.Printf("gorm example server listening http=%s udp=%s grpc=%s", *addr, *udpAddr, *grpcAddr)
	log.Fatal(app.ListenAndServe())
}
