package main

import (
	"log"
	"os"

	"gioui.org/app"
	"gioui.org/unit"
)

func main() {
	go func() {
		w := new(app.Window)
		w.Option(app.Title("SSH Tunnel Manager"))
		w.Option(app.Size(unit.Dp(600), unit.Dp(700)))
		if err := run(w); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

func run(w *app.Window) error {
	return RunUI(w)
}
