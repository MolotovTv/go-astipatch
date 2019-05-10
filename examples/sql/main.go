package main

import (
	"flag"

	"github.com/jmoiron/sqlx"
	"github.com/molotovtv/go-astilog"
	"github.com/molotovtv/go-astimysql"
	"github.com/molotovtv/go-astipatch"
	astiflag "github.com/molotovtv/go-astitools/flag"
)

func main() {
	// Subcommand
	var s = astiflag.Subcommand()
	flag.Parse()

	// Init logger
	astilog.SetLogger(astilog.New(astilog.FlagConfig()))

	// Init db
	var db *sqlx.DB
	var err error
	if db, err = astimysql.New(astimysql.FlagConfig()); err != nil {
		astilog.Fatal(err)
	}
	defer db.Close()

	// Init storer
	var st = astipatch.NewStorerSQL(db)

	// Init patcher
	var p = astipatch.NewPatcherSQL(db, st)

	// Load patches
	if err = p.Load(astipatch.FlagConfig()); err != nil {
		astilog.Fatal(err)
	}

	// Switch on subcommand
	switch s {
	case "init":
		if err = p.Init(); err != nil {
			astilog.Fatal(err)
		}
		astilog.Info("Init successful")
	case "patch":
		if err = p.Patch(); err != nil {
			astilog.Fatal(err)
		}
		astilog.Info("Patch successful")
	case "rollback":
		if err = p.Rollback(); err != nil {
			astilog.Fatal(err)
		}
		astilog.Info("Rollback successful")
	}
}
