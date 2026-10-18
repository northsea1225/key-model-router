package main

import (
	"fmt"
	"os"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: hashpw <password>")
		os.Exit(2)
	}
	hash, errHash := bcrypt.GenerateFromPassword([]byte(os.Args[1]), 12)
	if errHash != nil {
		fmt.Fprintln(os.Stderr, errHash)
		os.Exit(1)
	}
	fmt.Print(string(hash))
}
