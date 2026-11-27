package main

import (
	"log"

	"github.com/zhdsmy/branchline/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
