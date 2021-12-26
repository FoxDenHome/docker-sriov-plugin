package main

import (
	"log"
	"os"

	"github.com/docker/go-plugins-helpers/network"
	"github.com/urfave/cli"

	"github.com/Doridian/docker-sriov-plugin/driver"
)

var version = "DEV"

// Run initializes the driver
func Run(ctx *cli.Context) {
	d, err := driver.StartDriver()
	if err != nil {
		panic(err)
	}
	h := network.NewHandler(d)

	log.Printf("Mellanox sriov plugin started version=%v\n", version)
	log.Printf("Ready to accept commands.\n")

	err = h.ServeUnix("sriov", 0)
	if err != nil {
		log.Fatalf("Run app error: %s", err.Error())
		os.Exit(1)
	}
}

func main() {
	app := cli.NewApp()
	app.Name = "sriov"
	app.Usage = "Docker Networking using SRIOV/Passthrough netdevices"
	app.Version = version
	app.Flags = []cli.Flag{}
	app.Action = Run
	app.Run(os.Args)
}
