// milou: BYO-harness ACP router. One binary, every agent loop.
package main

import (
	"context"
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
	scan := flag.Bool("scan", false, "probe PATH for known harness binaries and exit")
	write := flag.Bool("write", false, "with --scan: emit drivers.d descriptors for found harnesses")
	test := flag.String("test", "", "run one canned prompt through DRIVER and print its event stream")
	flag.Parse()

	list := driver.Registry()
	drivers := map[string]driver.Driver{}
	order := []string{}
	for _, d := range list {
		drivers[d.Name()] = d
		order = append(order, d.Name())
	}

	switch {
	case *ver:
		fmt.Println("milou", version)
		return
	case *scan:
		for _, r := range driver.Scan() {
			status := "missing"
			if r.Path != "" {
				status = r.Path
			}
			fmt.Printf("%-12s %-10s %s\n", r.Name, r.Bin, status)
		}
		if *write {
			wrote, err := driver.WriteDescriptors()
			if err != nil {
				fmt.Fprintln(os.Stderr, "milou:", err)
				os.Exit(1)
			}
			for _, p := range wrote {
				fmt.Println("wrote", p)
			}
		}
		return
	case *check:
		for _, d := range list {
			probe := ""
			if pd, ok := d.(interface{ Binary() string }); ok {
				if _, err := exec.LookPath(pd.Binary()); err != nil {
					probe = " (binary not found: " + pd.Binary() + ")"
				}
			}
			fmt.Printf("driver %-10s registered%s\n", d.Name(), probe)
		}
		return
	case *test != "":
		d, ok := drivers[*test]
		if !ok {
			fmt.Fprintln(os.Stderr, "milou: unknown driver:", *test)
			os.Exit(1)
		}
		cwd, _ := os.Getwd()
		st, err := d.Start(context.Background(), "Reply with the single word: ok", cwd)
		if err != nil {
			fmt.Fprintln(os.Stderr, "milou:", err)
			os.Exit(1)
		}
		for ev := range st.Events() {
			fmt.Printf("%-12s %s\n", ev.Kind, ev.Text)
		}
		res, _ := st.Wait()
		fmt.Println("stop_reason:", res.StopReason, res.Error)
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
