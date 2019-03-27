package main

import (
	"grier/pdftext"

	"github.com/pkg/profile"
)

func main() {
	defer profile.Start(profile.MemProfile, profile.CPUProfile, profile.ProfilePath(".")).Stop()

	pdftext.Run()
}
