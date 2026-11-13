package portable

import (
	"context"
	"gopkg.in/yaml.v3"
	"path/filepath"
)

func (s *Service) Init(ctx context.Context, r InitRequest) (InitResult, error) {
	if r.Directory == "" || r.Project == "" {
		return InitResult{}, fail("invalid_config", "directory and project are required")
	}
	c := DefaultConfig()
	c.Project = r.Project
	c.Index.Source = r.IndexSource
	if c.Index.Source == "" {
		c.Index.Source = "index.md"
	}
	if r.Layout.Index != "" {
		c.Layout = r.Layout
	}
	if e := c.Validate(); e != nil {
		return InitResult{}, e
	}
	data, e := yaml.Marshal(c)
	if e != nil {
		return InitResult{}, e
	}
	index := []byte("# " + r.Project + "\n\n## Bundle inventory\n\n| ID | Document | Section | Path | Role |\n| --- | --- | --- | --- | --- |\n\nAdd document rows with stable IDs and ordinary canonical Google links.\nRoles: document, asset, asset-tree, reference. Assets and trees require an exact Path.\nEmbed images in native Docs; use Drive image links or declared asset trees in Markdown.\nThe empty starter inventory intentionally cannot plan or export.\n")
	files := map[string][]byte{"mdoc-bundle.yaml": data, "index.md": index}
	if e = s.outputStore().Install(ctx, r.Directory, files); e != nil {
		return InitResult{}, e
	}
	return InitResult{Files: []string{filepath.Join(r.Directory, "mdoc-bundle.yaml"), filepath.Join(r.Directory, "index.md")}}, nil
}
func (s *Service) outputStore() OutputStore {
	if s.options.OutputStore != nil {
		return s.options.OutputStore
	}
	return LocalOutputStore{}
}
