// Command sample draws a text label as the server does and writes its rows, for
// an app to build in as its sample:
//
//	go run ./firmware/tools/sample -family ptouch -media tape-24 "ESP32 OK" "PT-P700" > firmware/apps/ptp700/main/sample.rows
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/RolandWarburton/ptouch-print/internal/label"
	"github.com/RolandWarburton/ptouch-print/internal/wire"
)

func main() {
	family := flag.String("family", wire.FamilyLW450, "the printer's family")
	media := flag.String("media", "large-address", "the media's id")
	flag.Parse()
	m, ok := wire.FindMedia(*family, *media)
	if !ok {
		fmt.Fprintf(os.Stderr, "no media %s for %s\n", *media, *family)
		os.Exit(1)
	}
	r, err := label.Draw(wire.Job{Kind: wire.KindText, Lines: flag.Args(), Size: "medium", Align: "center"}, m)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Stdout.Write(r.Data)
}
