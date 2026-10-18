// milou: BYO-harness ACP router. One binary, every agent loop.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"

	"github.com/pierrebrunelle/milou/acp"
	"github.com/pierrebrunelle/milou/driver"
	"github.com/pierrebrunelle/milou/oai"
	"github.com/pierrebrunelle/milou/session"
)

var version = "0.1.0"

func main() {
	check := flag.Bool("check", false, "probe driver availability and exit")
	ver := flag.Bool("version", false, "print version and exit")
	serve := flag.String("serve", "", "run an OpenAI-compatible endpoint on ADDR (e.g. :9000) instead of ACP stdio")
	flag.Parse()

	list := []driver.Driver{
		driver.Mojo{}, driver.Pi{}, driver.Echo{},
	}
	drivers := map[string]driver.Driver{}
	order := []string{}
	for _, d := range list {
		drivers[d.Name()] = d
		order = append(order, d.Name())
	}

	if *ver {
		fmt.Println("milou", version)
		return
	}
	if *check {
		for _, d := range list {
			probe := ""
			if pd, ok := d.(interface{ Binary() string }); ok {
				if _, err := exec.LookPath(pd.Binary()); err != nil {
					probe = " (binary not found: " + pd.Binary() + ")"
				}
			}
			fmt.Printf("driver %-10s available%s\n", d.Name(), probe)
		}
		return
	}

	if *serve != "" {
		s := &oai.Server{Drivers: drivers, Fallback: "echo", Order: order}
		fmt.Fprintln(os.Stderr, "milou: OpenAI-compatible endpoint on", *serve)
		if err := http.ListenAndServe(*serve, s.Handler()); err != nil {
			fmt.Fprintln(os.Stderr, "milou:", err)
			os.Exit(1)
		}
		return
	}

	m := session.NewManager("echo", list...)
	if err := acp.NewServer(m).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "milou:", err)
		os.Exit(1)
	}
}
