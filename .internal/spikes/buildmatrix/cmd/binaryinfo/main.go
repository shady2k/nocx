// Command binaryinfo answers the one question §3 of the brief asks of a linked
// artifact -- is it statically linked -- without `file`, which is not installed
// here. It reads the real program headers and load commands through the
// standard library rather than shelling out: ELF has PT_INTERP and DT_NEEDED,
// and Mach-O has LC_LOAD_DYLIB.
package main

import (
	"debug/elf"
	"debug/macho"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// dynamicLibraries returns the libraries the loader must find for this file to
// start, and whether the file asks for a program interpreter at all. An ELF
// with neither is static; a Mach-O is never static -- it always binds against
// the system's dyld.
func dynamicLibraries(path string) (string, string, []string, error) {
	if f, err := elf.Open(path); err == nil {
		defer f.Close()
		var needed []string
		for _, p := range f.Progs {
			if p.Type == elf.PT_INTERP {
				b := make([]byte, p.Filesz)
				if _, err := p.ReadAt(b, 0); err == nil {
					needed = append(needed, "interp:"+strings.TrimRight(string(b), "\x00"))
				}
			}
		}
		if libs, err := f.DynString(elf.DT_NEEDED); err == nil {
			needed = append(needed, libs...)
		}
		kind := "static"
		if len(needed) > 0 {
			kind = "dynamic"
		}
		return "elf", kind, needed, nil
	}
	if f, err := macho.Open(path); err == nil {
		defer f.Close()
		var libs []string
		for _, l := range f.Loads {
			if d, ok := l.(*macho.Dylib); ok {
				libs = append(libs, d.Name)
			}
		}
		return "macho", "dynamic", libs, nil
	}
	return "", "", nil, fmt.Errorf("%s: neither ELF nor Mach-O", path)
}

func main() {
	type row struct {
		Path    string   `json:"path"`
		Format  string   `json:"format"`
		Linkage string   `json:"linkage"`
		Systems []string `json:"system_libraries"`
		Bytes   int64    `json:"bytes"`
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	rows := make([]row, 0, len(os.Args)-1)
	for _, p := range os.Args[1:] {
		format, linkage, libs, err := dynamicLibraries(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "binaryinfo:", err)
			os.Exit(1)
		}
		fi, err := os.Stat(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "binaryinfo:", err)
			os.Exit(1)
		}
		rows = append(rows, row{
			Path: filepath.Base(p), Format: format, Linkage: linkage,
			Systems: libs, Bytes: fi.Size(),
		})
	}
	if err := enc.Encode(rows); err != nil {
		fmt.Fprintln(os.Stderr, "binaryinfo:", err)
		os.Exit(1)
	}
}
