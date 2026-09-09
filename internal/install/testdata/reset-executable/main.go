package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "daemon" {
		fmt.Println("ready")
		for {
			time.Sleep(time.Hour)
		}
	}
	fmt.Println("reset executable fixture")
}
