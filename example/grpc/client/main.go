package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/saramand9/bignet/kpro/kprogrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9090", "server address")
	name := flag.String("name", "kpro", "name to send")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, *addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	client := kprogrpc.NewGreeterClient(conn)

	hello, err := client.SayHello(ctx, wrapperspb.String(*name))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("SayHello:", hello.Value)

	goodbye, err := client.SayGoodbye(ctx, wrapperspb.String(*name))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("SayGoodbye:", goodbye.Value)

	length, err := client.GetNameLength(ctx, wrapperspb.String(*name))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("GetNameLength:", length.Value)

	echo, err := client.EchoBytes(ctx, wrapperspb.Bytes([]byte(*name)))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("EchoBytes:", string(echo.Value))
}
