package main

import (
	"debug/elf"
	"debug/macho"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// inspect answers the questions the probe script asks of a built file without
// `file` and without readelf: is this ELF or Mach-O, does it ask the loader for
// anything at all, and does it contain the symbol the archive was linked for.
//
// The last one is the difference between "it linked" and "it linked THE
// ARCHIVE". A static archive contributes nothing to a link whose symbols
// nothing references, and libghostty-vt's absent -lresolv failure is Go's own
// flag rather than proof the library was used — so the probe asserts that a
// ghostty symbol is IN the artifact, and this is what reads it out.
type inspection struct {
	Path        string          `json:"path"`
	Format      string          `json:"format"`
	Static      bool            `json:"static"`
	Interpreter []string        `json:"interpreter,omitempty"`
	Libraries   []string        `json:"libraries,omitempty"`
	SymbolTable bool            `json:"symbol_table"`
	Symbols     map[string]bool `json:"symbols,omitempty"`
	Bytes       int64           `json:"bytes"`
}

type symbolsFlag []string

func (s *symbolsFlag) String() string { return strings.Join(*s, ",") }

func (s *symbolsFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func cmdInspect(args []string) error {
	fs := flags("inspect")
	requireStatic := fs.Bool("require-static", false, "fail unless every given file is statically linked")
	var want symbolsFlag
	fs.Var(&want, "symbol", "a symbol that must be defined in every given file (repeatable)")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		return errors.New("inspect: no files given")
	}
	rows := make([]inspection, 0, fs.NArg())
	for _, path := range fs.Args() {
		row, err := inspectFile(path)
		if err != nil {
			return err
		}
		row.Symbols = make(map[string]bool, len(want))
		for _, name := range want {
			found, err := definedSymbol(path, name)
			if err != nil {
				return err
			}
			row.Symbols[name] = found
		}
		rows = append(rows, row)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rows); err != nil {
		return err
	}
	for _, row := range rows {
		if *requireStatic && !row.Static {
			return fmt.Errorf("%s: not statically linked (interpreter %v, libraries %v)", row.Path, row.Interpreter, row.Libraries)
		}
		for _, name := range want {
			if !row.Symbols[name] {
				if !row.SymbolTable {
					return fmt.Errorf("%s: no symbol table, so %q cannot be confirmed — build it without -s", row.Path, name)
				}
				return fmt.Errorf("%s: %q is not defined; the archive was not linked in", row.Path, name)
			}
		}
	}
	return nil
}

func inspectFile(path string) (inspection, error) {
	info, err := os.Stat(path)
	if err != nil {
		return inspection{}, err
	}
	row := inspection{Path: path, Bytes: info.Size()}
	if f, err := elf.Open(path); err == nil {
		defer func() { _ = f.Close() }()
		row.Format = "elf"
		for _, p := range f.Progs {
			if p.Type != elf.PT_INTERP {
				continue
			}
			b := make([]byte, p.Filesz)
			if _, err := p.ReadAt(b, 0); err == nil {
				row.Interpreter = append(row.Interpreter, strings.TrimRight(string(b), "\x00"))
			}
		}
		if libs, err := f.DynString(elf.DT_NEEDED); err == nil {
			row.Libraries = append(row.Libraries, libs...)
		}
		if syms, err := f.Symbols(); err == nil && len(syms) > 0 {
			row.SymbolTable = true
		}
		if syms, err := f.DynamicSymbols(); err == nil && len(syms) > 0 {
			row.SymbolTable = true
		}
		row.Static = len(row.Interpreter) == 0 && len(row.Libraries) == 0
		return row, nil
	}
	if f, err := macho.Open(path); err == nil {
		defer func() { _ = f.Close() }()
		row.Format = "macho"
		// A Mach-O is never static: it always binds against the system's
		// dyld, which is why "static" was never a property macOS held
		// (Makefile's helpers comment, measured in nocx-cm1ac).
		for _, l := range f.Loads {
			if d, ok := l.(*macho.Dylib); ok {
				row.Libraries = append(row.Libraries, d.Name)
			}
		}
		row.SymbolTable = f.Symtab != nil
		return row, nil
	}
	if f, err := macho.OpenFat(path); err == nil {
		defer func() { _ = f.Close() }()
		row.Format = fmt.Sprintf("macho-fat(%d)", len(f.Arches))
		for _, a := range f.Arches {
			for _, l := range a.Loads {
				if d, ok := l.(*macho.Dylib); ok {
					row.Libraries = appendUnique(row.Libraries, d.Name)
				}
			}
			if a.Symtab != nil {
				row.SymbolTable = true
			}
		}
		return row, nil
	}
	return inspection{}, fmt.Errorf("%s: neither ELF nor Mach-O", path)
}

// definedSymbol reports whether a symbol is defined (not undefined) in the
// file. A stripped binary has no table at all, and the caller says so rather
// than reading absence as "not linked".
func definedSymbol(path, name string) (bool, error) {
	if f, err := elf.Open(path); err == nil {
		defer func() { _ = f.Close() }()
		tables := [][]elf.Symbol{}
		if syms, err := f.Symbols(); err == nil {
			tables = append(tables, syms)
		}
		if syms, err := f.DynamicSymbols(); err == nil {
			tables = append(tables, syms)
		}
		for _, table := range tables {
			for _, s := range table {
				if s.Name == name && s.Section != elf.SHN_UNDEF {
					return true, nil
				}
			}
		}
		return false, nil
	}
	if f, err := macho.Open(path); err == nil {
		defer func() { _ = f.Close() }()
		return machoSymbol(f, name), nil
	}
	if f, err := macho.OpenFat(path); err == nil {
		defer func() { _ = f.Close() }()
		for _, a := range f.Arches {
			if machoSymbol(a.File, name) {
				return true, nil
			}
		}
		return false, nil
	}
	return false, fmt.Errorf("%s: neither ELF nor Mach-O", path)
}

func machoSymbol(f *macho.File, name string) bool {
	if f.Symtab == nil {
		return false
	}
	// Mach-O prefixes C symbols with an underscore, and a Cgo wrapper's
	// symbol may be either — so both spellings are tried rather than making
	// every caller know which side of the CGo boundary it is asking about.
	for _, cand := range []string{name, "_" + name} {
		for _, s := range f.Symtab.Syms {
			if s.Name == cand && s.Sect != 0 {
				return true
			}
		}
	}
	return false
}

func appendUnique(list []string, value string) []string {
	for _, have := range list {
		if have == value {
			return list
		}
	}
	return append(list, value)
}
