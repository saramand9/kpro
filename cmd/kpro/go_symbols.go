package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"strings"

	"github.com/saramand9/kpro/internal/codegen"
)

type goSymbolOrigin struct {
	source string
	file   string
}

type goPackageOrigin struct {
	name      string
	directory string
	source    string
	file      string
}

type goSymbolRegistry struct {
	packages map[string]goPackageOrigin
	symbols  map[string]map[string]goSymbolOrigin
}

func newGoSymbolRegistry() *goSymbolRegistry {
	return &goSymbolRegistry{
		packages: make(map[string]goPackageOrigin),
		symbols:  make(map[string]map[string]goSymbolOrigin),
	}
}

func (r *goSymbolRegistry) Add(source string, files []codegen.File) error {
	for _, file := range files {
		if !strings.EqualFold(path.Ext(file.Path), ".go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file.Path, file.Content, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parse planned Go output %q: %w", file.Path, err)
		}
		directory := path.Dir(file.Path)
		directoryKey := directory
		if directory != "." {
			directoryKey, err = codegen.PortablePathKey(directory)
			if err != nil {
				return fmt.Errorf("derive portable Go package key for %q: %w", directory, err)
			}
		}
		if previous, exists := r.packages[directoryKey]; exists && previous.name != parsed.Name.Name {
			return fmt.Errorf(
				"Go package directory %q uses package %q in source %q (%s), conflicting with aliased directory %q using package %q in source %q (%s)",
				directory,
				parsed.Name.Name,
				source,
				file.Path,
				previous.directory,
				previous.name,
				previous.source,
				previous.file,
			)
		}
		if _, exists := r.packages[directoryKey]; !exists {
			r.packages[directoryKey] = goPackageOrigin{name: parsed.Name.Name, directory: directory, source: source, file: file.Path}
		}
		if r.symbols[directoryKey] == nil {
			r.symbols[directoryKey] = make(map[string]goSymbolOrigin)
		}
		for _, declaration := range parsed.Decls {
			for _, symbol := range plannedGoDeclarationSymbols(declaration) {
				if symbol == "" {
					continue
				}
				if previous, exists := r.symbols[directoryKey][symbol]; exists {
					return fmt.Errorf(
						"Go symbol %q in package directory %q from source %q (%s) conflicts with source %q (%s)",
						strings.TrimPrefix(symbol, "method:"),
						directory,
						source,
						file.Path,
						previous.source,
						previous.file,
					)
				}
				r.symbols[directoryKey][symbol] = goSymbolOrigin{source: source, file: file.Path}
			}
		}
	}
	return nil
}

func plannedGoDeclarationSymbols(declaration ast.Decl) []string {
	switch declaration := declaration.(type) {
	case *ast.GenDecl:
		var result []string
		for _, spec := range declaration.Specs {
			switch spec := spec.(type) {
			case *ast.TypeSpec:
				if spec.Name.Name != "_" {
					result = append(result, spec.Name.Name)
				}
			case *ast.ValueSpec:
				for _, name := range spec.Names {
					if name.Name != "_" {
						result = append(result, name.Name)
					}
				}
			}
		}
		return result
	case *ast.FuncDecl:
		if declaration.Recv == nil {
			if declaration.Name.Name == "init" {
				return nil
			}
			return []string{declaration.Name.Name}
		}
		receiver := plannedGoReceiverName(declaration.Recv.List[0].Type)
		if receiver == "" {
			return nil
		}
		return []string{"method:" + receiver + "." + declaration.Name.Name}
	default:
		return nil
	}
}

func plannedGoReceiverName(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name
	case *ast.StarExpr:
		return plannedGoReceiverName(expression.X)
	case *ast.IndexExpr:
		return plannedGoReceiverName(expression.X)
	case *ast.IndexListExpr:
		return plannedGoReceiverName(expression.X)
	case *ast.ParenExpr:
		return plannedGoReceiverName(expression.X)
	default:
		return ""
	}
}
