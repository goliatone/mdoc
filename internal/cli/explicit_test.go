package cli

import "testing"

func TestExplicitFlagsRecordsOnlyPresentLongFlags(t *testing.T) {
	flags := explicitFlags([]string{"status", "--quiet=false", "README.md", "--profile", "review", "--", "--json"})
	if !flags["quiet"] || !flags["profile"] {
		t.Fatalf("flags = %#v", flags)
	}
	if flags["json"] {
		t.Fatalf("flag after terminator was recorded: %#v", flags)
	}
	if len(flags) != 2 {
		t.Fatalf("unexpected explicit flags: %#v", flags)
	}
}

func TestCommonOptionsIncludeOnlyExplicitOverrides(t *testing.T) {
	flags := CommonFlags{
		Quiet: true, Sources: []string{"docs/*.md"}, Entry: "docs/README.md",
		Output: "human", TitlePolicy: "required", HeadingJumps: "error", LinkPolicy: "error",
	}
	options := flags.options(map[string]bool{"quiet": true, "source": true})
	output, ok := options.Overrides["output"].(map[string]any)
	if !ok || output["quiet"] != true {
		t.Fatalf("output overrides = %#v", options.Overrides)
	}
	if _, exists := output["format"]; exists {
		t.Fatalf("omitted output flag masked config: %#v", options.Overrides)
	}
	profile := options.Overrides["profile"].(map[string]any)
	sources := profile["sources"].(map[string]any)["include"].([]string)
	if len(sources) != 1 || sources[0] != "docs/*.md" {
		t.Fatalf("source override = %#v", sources)
	}
	if _, exists := profile["entry"]; exists {
		t.Fatalf("omitted entry flag masked config: %#v", options.Overrides)
	}

	options = CommonFlags{Quiet: false}.options(map[string]bool{"quiet": true})
	output = options.Overrides["output"].(map[string]any)
	if value, exists := output["quiet"]; !exists || value != false {
		t.Fatalf("explicit false was not preserved: %#v", output)
	}
	options = CommonFlags{JSON: false}.options(map[string]bool{"json": true})
	output = options.Overrides["output"].(map[string]any)
	if output["format"] != "human" {
		t.Fatalf("explicit json=false was not preserved: %#v", output)
	}
}

func TestClearableListTreatsOneEmptyFlagAsClear(t *testing.T) {
	values := clearableList([]string{""})
	if values == nil || len(values) != 0 {
		t.Fatalf("clearable list = %#v", values)
	}
}
