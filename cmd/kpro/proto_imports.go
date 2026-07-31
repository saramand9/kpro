package main

import (
	"fmt"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
	"github.com/saramand9/kpro/internal/codegen/protogen"
)

func snapshotProtoImportClosure(
	value manifest.Manifest,
	capture func(string, bool) (addFileState, error),
) error {
	queue := make([]string, 0)
	for _, source := range value.Sources {
		if source.Kind == manifest.KindProto && source.Plugin == nil {
			queue = append(queue, source.Path)
		}
	}
	visited := make(map[string]struct{}, len(queue))
	for len(queue) != 0 {
		name := queue[0]
		queue = queue[1:]
		portable, err := codegen.SafeRelativePath(name)
		if err != nil {
			return fmt.Errorf("unsafe proto dependency %q: %w", name, err)
		}
		if _, exists := visited[portable]; exists {
			continue
		}
		visited[portable] = struct{}{}
		state, err := capture(portable, true)
		if err != nil {
			return fmt.Errorf("capture proto dependency %q: %w", portable, err)
		}
		imports, err := protogen.ParseImports(state.content)
		if err != nil {
			return fmt.Errorf("parse imports in %q: %w", portable, err)
		}
		queue = append(queue, imports...)
	}
	return nil
}
