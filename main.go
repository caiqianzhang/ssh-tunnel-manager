package main

import (
	"log"
	"os"

	"gioui.org/app"
)

func main() {
	go func() {
		w := new(app.Window)
		if err := run(w); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

func run(w *app.Window) error {
	// TODO: UI loop will be added in Task 5
	return nil
}
