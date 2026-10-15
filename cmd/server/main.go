package main

import (
	"context"
	"github.com/PaNasMs/module-backup/internal/backup"
	"github.com/PaNasMs/module-sdk/modulehost"
	"log"
	"net/http"
)

func main() {
	engine, err := backup.New("/var/lib/panasms-backup")
	if err != nil {
		log.Fatal(err)
	}
	if err = engine.RecoverApplications(); err != nil {
		log.Print(err)
	}
	go engine.Schedule(context.Background())
	modulehost.ServeWithPassivePaths("backup", engine.Active, map[string]bool{"/state": true}, func(allowed map[string]bool) http.Handler { return engine.Handler(allowed) })
}
