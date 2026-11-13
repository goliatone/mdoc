package portable

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
)

// PandocConverter uses the repository's GFM/Pandoc representation with bounded
// in-memory stdin/stdout, so source text never becomes a command argument.
type PandocConverter struct{ Binary string }
type boundedBuffer struct {
	bytes.Buffer
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, fail("limit_exceeded", "conversion exceeds size limit")
	}
	return b.Buffer.Write(p)
}
func (p PandocConverter) run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	bin := p.Binary
	if bin == "" {
		bin = "pandoc"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = bytes.NewReader(input)
	out := &boundedBuffer{max: 100 << 20}
	cmd.Stdout = out
	cmd.Stderr = &boundedBuffer{max: 4096}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fail("unsupported_content", "Pandoc conversion failed")
	}
	return out.Bytes(), nil
}
func (p PandocConverter) Parse(ctx context.Context, b []byte) (map[string]any, error) {
	raw, e := p.run(ctx, b, "--from=gfm-autolink_bare_uris", "--to=json")
	if e != nil {
		return nil, e
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil, fail("unsupported_content", "invalid Pandoc output")
	}
	return m, nil
}
func (p PandocConverter) Render(ctx context.Context, m map[string]any) ([]byte, error) {
	b, e := json.Marshal(m)
	if e != nil {
		return nil, e
	}
	return p.run(ctx, b, "--from=json", "--to=gfm", "--wrap=none")
}
func walk(v any, fn func(map[string]any)) {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["t"]; ok {
			fn(x)
			walk(x["c"], fn)
		} else if b, ok := x["blocks"]; ok {
			walk(b, fn)
		}
	case []any:
		for _, c := range x {
			walk(c, fn)
		}
	}
}
func arr(v any) []any  { x, _ := v.([]any); return x }
func str(v any) string { x, _ := v.(string); return x }
func num(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	}
	return 0
}
func textOf(v any) string {
	var b strings.Builder
	walk(v, func(n map[string]any) {
		switch n["t"] {
		case "Str":
			b.WriteString(str(n["c"]))
		case "Space", "SoftBreak", "LineBreak":
			b.WriteByte(' ')
		case "Code":
			c := arr(n["c"])
			if len(c) > 1 {
				b.WriteString(str(c[1]))
			}
		}
	})
	return strings.TrimSpace(b.String())
}
func target(n map[string]any) string {
	c := arr(n["c"])
	if len(c) < 3 {
		return ""
	}
	t := arr(c[2])
	if len(t) < 1 {
		return ""
	}
	return str(t[0])
}
func setTarget(n map[string]any, s string)   { c := arr(n["c"]); t := arr(c[2]); t[0] = s }
func astNode(t string, c any) map[string]any { return map[string]any{"t": t, "c": c} }
func plain(s string) []any {
	out := []any{}
	for i, w := range strings.Fields(s) {
		if i > 0 {
			out = append(out, astNode("Space", nil))
		}
		out = append(out, astNode("Str", w))
	}
	return out
}
func attr() []any { return []any{"", []any{}, []any{}} }
