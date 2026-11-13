package cli

import (
	"fmt"
	"github.com/goliatone/mdoc/internal/app"
	"github.com/goliatone/mdoc/portable"
	"os/signal"
	"syscall"
)

type BundleCmd struct {
	Init   BundleInitCmd   `cmd:"" help:"Create local bundle conventions and an empty index."`
	Plan   BundlePlanCmd   `cmd:"" help:"Read the index and preview destinations without exporting."`
	Export BundleExportCmd `cmd:"" help:"Export read-only Drive content into a new local directory."`
	Verify BundleVerifyCmd `cmd:"" help:"Verify an existing bundle offline without selecting credentials."`
}
type BundleInitCmd struct {
	Directory   string `required:"" help:"New directory for local authoring conventions."`
	Project     string `required:"" help:"Project identity."`
	IndexSource string `help:"Canonical index URL/ID; defaults to a local index.md."`
	JSON        bool   `help:"Print JSON."`
}
type BundleConfigFlags struct {
	Config string `default:"mdoc-bundle.yaml" help:"Portable bundle configuration file."`
	JSON   bool   `help:"Print JSON."`
}
type BundlePlanCmd struct{ BundleConfigFlags }
type BundleExportCmd struct {
	BundleConfigFlags
	Output string `required:"" help:"New bundle destination; existing paths are rejected."`
}
type BundleVerifyCmd struct {
	Directory string `arg:"" help:"Bundle directory."`
	JSON      bool   `help:"Print JSON."`
}

func bundleService(r *Runtime, offline bool) (portable.API, error) {
	if r.Portable != nil {
		return r.Portable, nil
	}
	if offline {
		return portable.New(portable.Options{})
	}
	credentials, _ := r.Auth.(app.ClientProvider)
	return app.PortableService(r.Context, credentials)
}
func (c BundleInitCmd) Run(r *Runtime) error {
	s, e := bundleService(r, true)
	if e != nil {
		return e
	}
	result, e := s.Init(r.Context, portable.InitRequest{Directory: c.Directory, Project: c.Project, IndexSource: c.IndexSource})
	if e != nil {
		return e
	}
	if c.JSON {
		return writeJSON(r.Out, result)
	}
	for _, p := range result.Files {
		if _, e = fmt.Fprintln(r.Out, p); e != nil {
			return e
		}
	}
	return nil
}
func (c BundlePlanCmd) Run(r *Runtime) error {
	config, e := portable.LoadConfig(c.Config)
	if e != nil {
		return e
	}
	s, e := bundleService(r, false)
	if e != nil {
		return e
	}
	result, e := s.Plan(r.Context, portable.PlanRequest{Config: config})
	if e != nil {
		return e
	}
	if c.JSON {
		return writeJSON(r.Out, result)
	}
	for _, p := range result.Destinations {
		suffix := ""
		if p.PendingEquality {
			suffix = " (byte equality pending)"
		}
		if _, e = fmt.Fprintf(r.Out, "%s\t%s%s\n", p.ID, p.Path, suffix); e != nil {
			return e
		}
	}
	for _, d := range result.Diagnostics {
		if _, e = fmt.Fprintf(r.Out, "%s: %s\n", d.Code, d.Message); e != nil {
			return e
		}
	}
	return nil
}
func (c BundleExportCmd) Run(r *Runtime) error {
	config, e := portable.LoadConfig(c.Config)
	if e != nil {
		return e
	}
	s, e := bundleService(r, false)
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(r.Context, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	result, e := s.Export(ctx, portable.ExportRequest{Config: config, Destination: c.Output})
	if e != nil {
		return e
	}
	if c.JSON {
		return writeJSON(r.Out, result)
	}
	_, e = fmt.Fprintf(r.Out, "Exported %s/%s\nLocal complete: %s\nNo online dependencies: %s\n", c.Output, result.EntryPath, yesNo(result.Manifest.LocalComplete), yesNo(result.Manifest.NoOnlineDependencies))
	if e != nil {
		return e
	}
	for _, d := range result.Diagnostics {
		if _, e = fmt.Fprintf(r.Out, "%s: %s\n", d.Code, d.Message); e != nil {
			return e
		}
	}
	return nil
}
func (c BundleVerifyCmd) Run(r *Runtime) error {
	s, e := bundleService(r, true)
	if e != nil {
		return e
	}
	result, e := s.Verify(r.Context, portable.VerifyRequest{Directory: c.Directory})
	if e != nil {
		return e
	}
	if c.JSON {
		return writeJSON(r.Out, result)
	}
	if _, e = fmt.Fprintf(r.Out, "Local complete: %s\nNo online dependencies: %s\n", yesNo(result.LocalComplete), yesNo(result.NoOnlineDependencies)); e != nil {
		return e
	}
	for _, d := range result.Diagnostics {
		if _, e = fmt.Fprintf(r.Out, "%s: %s\n", d.Code, d.Message); e != nil {
			return e
		}
	}
	return nil
}
