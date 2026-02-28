package main

import "os"

// defaultGBProject returns KD_PROJECT > BOAT_PROJECT > "".
func defaultGBProject() string {
	if p := os.Getenv("KD_PROJECT"); p != "" {
		return p
	}
	return os.Getenv("BOAT_PROJECT")
}
