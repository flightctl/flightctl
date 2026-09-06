package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
)

func parseGoSrc(filename, src string) (*ast.File, error) {
	return parser.ParseFile(token.NewFileSet(), filename, src, 0)
}

func parseObservabilityOnlyImages(src string) (map[string]struct{}, error) {
	f, err := parseGoSrc("main.go", src)
	if err != nil {
		return nil, fmt.Errorf("parse generate-embed/main.go: %w", err)
	}
	got := map[string]struct{}{}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		match := false
		for _, name := range vs.Names {
			if name.Name == "observabilityOnlyImages" {
				match = true
				found = true
				break
			}
		}
		if !match {
			return true
		}
		for _, val := range vs.Values {
			lit, ok := val.(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.BasicLit)
				if !ok || key.Kind != token.STRING {
					continue
				}
				name, err := strconv.Unquote(key.Value)
				if err != nil {
					continue
				}
				ident, ok := kv.Value.(*ast.Ident)
				if !ok || ident.Name != "true" {
					continue
				}
				got[name] = struct{}{}
			}
		}
		return false
	})
	if !found {
		return nil, fmt.Errorf("could not find observabilityOnlyImages in generate-embed/main.go")
	}
	return got, nil
}

func parseRendererTagOverrides(src string) (map[string]struct{}, error) {
	f, err := parseGoSrc("renderer.go", src)
	if err != nil {
		return nil, fmt.Errorf("parse renderer.go: %w", err)
	}

	keyByField := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "RendererConfig" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			ident, ok := field.Type.(*ast.Ident)
			if !ok || ident.Name != "ImageConfig" || field.Tag == nil {
				continue
			}
			tag := reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Get("mapstructure")
			for _, name := range field.Names {
				keyByField[name.Name] = tag
			}
		}
		return false
	})
	if len(keyByField) == 0 {
		return nil, fmt.Errorf("could not find RendererConfig ImageConfig fields")
	}

	got := map[string]struct{}{}
	foundFn := false
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "ApplyFlightctlServicesTagOverride" || fn.Body == nil {
			return true
		}
		foundFn = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
				return true
			}
			rhs, ok := as.Rhs[0].(*ast.Ident)
			if !ok || rhs.Name != "tag" {
				return true
			}
			sel, ok := as.Lhs[0].(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Tag" {
				return true
			}
			inner, ok := sel.X.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if key, ok := keyByField[inner.Sel.Name]; ok {
				got[key] = struct{}{}
			}
			return true
		})
		return false
	})
	if !foundFn {
		return nil, fmt.Errorf("could not find ApplyFlightctlServicesTagOverride")
	}
	return got, nil
}
