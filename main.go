package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/cbartram/hearthhub-mod-api/server"
	"github.com/joho/godotenv"
	"log"
	"os"
)

func main() {
	ginMode := os.Getenv("GIN_MODE")

	port := flag.String("port", "8080", "port to listen on")
	flag.Parse()

	// Running locally
	if ginMode == "" {
		err := godotenv.Load()
		if err != nil {
			log.Fatalf(fmt.Sprintf("error loading .env file: %v", err))
		}
	}

	err := server.NewRouter(context.Background()).Run(fmt.Sprintf(":%v", *port))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Server Listening on port %s", *port)
}
