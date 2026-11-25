// Command code128ref prints the modules (1 = bar) of a Code 128 barcode for each
// argument, as the Go library the server uses draws them. The board's encoder
// is checked against this.
package main

import (
	"fmt"
	"os"

	"github.com/boombuler/barcode/code128"
)

func main() {
	for _, s := range os.Args[1:] {
		bc, err := code128.Encode(s)
		if err != nil {
			fmt.Println(s, "error", err)
			continue
		}
		fmt.Printf("%s ", s)
		for x := 0; x < bc.Bounds().Dx(); x++ {
			if r, _, _, _ := bc.At(x, 0).RGBA(); r < 0x8000 {
				fmt.Print("1")
			} else {
				fmt.Print("0")
			}
		}
		fmt.Println()
	}
}
