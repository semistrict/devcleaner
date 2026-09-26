package main

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestForwardedPathsRetainCallerDirectory(t *testing.T) {
	t.Setenv("DEVCLEANER_DB", "relative.db")
	input := []string{"scan", "--root", "projects", "--path=projects/node_modules"}
	got, err := forwardedArgs(input)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.Abs("projects")
	path, _ := filepath.Abs("projects/node_modules")
	database, _ := filepath.Abs("relative.db")
	want := []string{"scan", "--root", root, "--path=" + path, "--db", database}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
	if input[2] != "projects" {
		t.Fatal("forwarding mutated original arguments")
	}
	explicit, err := forwardedArgs([]string{"status", "--db=custom.db"})
	if err != nil || len(explicit) != 2 {
		t.Fatal("explicit database should override environment")
	}
}
