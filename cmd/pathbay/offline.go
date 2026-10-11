package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/lsy223622/PathBay/internal/pathbay"
	"os"
	"strings"
)

func offline(args []string) bool {
	if len(args) == 0 || (args[0] != "password" && args[0] != "token") {
		return false
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "use password reset or token create/list/revoke")
		os.Exit(1)
	}
	fs := flag.NewFlagSet(args[0], flag.ExitOnError)
	dir := fs.String("data-dir", env("PATHBAY_DATA_DIR", "data"), "instance data directory")
	name := fs.String("name", "", "token name")
	remaining := args[2:]
	id := ""
	if args[0] == "token" && args[1] == "revoke" && len(remaining) > 0 {
		id = remaining[0]
		remaining = remaining[1:]
	}
	fs.Parse(remaining)
	a, err := pathbay.Open(pathbay.Options{DataDir: *dir})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer a.Close()
	switch args[0] + " " + args[1] {
	case "password reset":
		reader := bufio.NewReader(os.Stdin)
		fmt.Fprint(os.Stderr, "New password: ")
		first, _ := reader.ReadString('\n')
		fmt.Fprint(os.Stderr, "Confirm password: ")
		second, _ := reader.ReadString('\n')
		first = strings.TrimRight(first, "\r\n")
		second = strings.TrimRight(second, "\r\n")
		if first != second {
			err = fmt.Errorf("passwords differ")
		} else {
			err = a.ResetPassword(first)
		}
	case "token create":
		var token pathbay.Token
		var value string
		token, value, err = a.CreateToken(*name)
		if err == nil {
			json.NewEncoder(os.Stdout).Encode(map[string]any{"id": token.ID, "name": token.Name, "token": value})
		}
	case "token list":
		err = json.NewEncoder(os.Stdout).Encode(a.ListTokens())
	case "token revoke":
		err = a.RevokeToken(id)
	default:
		err = fmt.Errorf("unknown offline command")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return true
}
