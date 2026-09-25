package fields

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
	"time"
)

type SegmentKind string

const (
	SegmentText      SegmentKind = "text"
	SegmentPage      SegmentKind = "page_number"
	SegmentPageCount SegmentKind = "page_count"
)

type Segment struct {
	Kind SegmentKind `json:"kind"`
	Text string      `json:"text,omitempty"`
}

type TemplateContext struct {
	PublicationID   string
	Fields          map[string]any
	Computed        map[string]any
	AllowPageFields bool
}

type Template struct {
	name string
	tree *parse.Tree
}

type pageValue SegmentKind

var safeTemplateFunctions = template.FuncMap{
	"field":    func(...any) any { return nil },
	"optional": func(...any) any { return nil },
	"mdoc":     func(...any) any { return nil },
	"page":     func(...any) any { return nil },
	"date":     func(...any) any { return nil },
	"number":   func(...any) any { return nil },
	"upper":    func(...any) any { return nil },
	"lower":    func(...any) any { return nil },
	"join":     func(...any) any { return nil },
	"eq":       func(...any) any { return nil },
	"ne":       func(...any) any { return nil },
	"and":      func(...any) any { return nil },
	"or":       func(...any) any { return nil },
	"not":      func(...any) any { return nil },
	"len":      func(...any) any { return nil },
}

func ParseTemplate(name, source string) (*Template, error) {
	parsed, err := template.New(name).Funcs(safeTemplateFunctions).Option("missingkey=error").Parse(source)
	if err != nil {
		return nil, err
	}
	if parsed.Tree == nil || parsed.Tree.Root == nil {
		return nil, errors.New("template has no parse tree")
	}
	return &Template{name: name, tree: parsed.Tree}, nil
}

func (templateValue *Template) Evaluate(context TemplateContext) ([]Segment, error) {
	root := map[string]any{"fields": context.Fields, "mdoc": context.Computed}
	evaluator := templateEvaluator{context: context, variables: map[string]any{"$": root}}
	segments, err := evaluator.nodes(templateValue.tree.Root, root)
	if err != nil {
		return nil, fmt.Errorf("publication %q template %q: %w", context.PublicationID, templateValue.name, err)
	}
	return compactSegments(segments), nil
}

type templateEvaluator struct {
	context   TemplateContext
	variables map[string]any
}

func (e templateEvaluator) nodes(list *parse.ListNode, dot any) ([]Segment, error) {
	if list == nil {
		return nil, nil
	}
	segments := []Segment{}
	for _, node := range list.Nodes {
		switch current := node.(type) {
		case *parse.TextNode:
			segments = append(segments, Segment{Kind: SegmentText, Text: string(current.Text)})
		case *parse.ActionNode:
			value, err := e.pipeline(current.Pipe, dot)
			if err != nil {
				return nil, err
			}
			if len(current.Pipe.Decl) > 0 {
				for _, declaration := range current.Pipe.Decl {
					e.variables[declaration.Ident[0]] = value
				}
				continue
			}
			segment, err := valueSegment(value)
			if err != nil {
				return nil, err
			}
			segments = append(segments, segment...)
		case *parse.IfNode:
			value, err := e.pipeline(current.Pipe, dot)
			if err != nil {
				return nil, err
			}
			if _, page := value.(pageValue); page {
				return nil, errors.New("page fields must be emitted directly and cannot control a condition")
			}
			branch := current.ElseList
			if truth(value) {
				branch = current.List
			}
			child, err := e.nodes(branch, dot)
			if err != nil {
				return nil, err
			}
			segments = append(segments, child...)
		case *parse.WithNode:
			value, err := e.pipeline(current.Pipe, dot)
			if err != nil {
				return nil, err
			}
			if _, page := value.(pageValue); page {
				return nil, errors.New("page fields must be emitted directly and cannot control with")
			}
			branch := current.ElseList
			branchDot := dot
			if truth(value) {
				branch = current.List
				branchDot = value
			}
			child, err := e.nodes(branch, branchDot)
			if err != nil {
				return nil, err
			}
			segments = append(segments, child...)
		case *parse.RangeNode:
			child, err := e.rangeNode(current, dot)
			if err != nil {
				return nil, err
			}
			segments = append(segments, child...)
		case *parse.CommentNode:
			continue
		default:
			return nil, fmt.Errorf("template node %T is not supported", node)
		}
	}
	return segments, nil
}

func (e templateEvaluator) rangeNode(node *parse.RangeNode, dot any) ([]Segment, error) {
	value, err := e.pipeline(node.Pipe, dot)
	if err != nil {
		return nil, err
	}
	if _, page := value.(pageValue); page {
		return nil, errors.New("page fields must be emitted directly and cannot control a range")
	}
	items, err := rangeItems(value)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return e.nodes(node.ElseList, dot)
	}
	segments := []Segment{}
	for _, item := range items {
		child := e.clone()
		if len(node.Pipe.Decl) == 1 {
			child.variables[node.Pipe.Decl[0].Ident[0]] = item.value
		} else if len(node.Pipe.Decl) >= 2 {
			child.variables[node.Pipe.Decl[0].Ident[0]] = item.key
			child.variables[node.Pipe.Decl[1].Ident[0]] = item.value
		}
		part, partErr := child.nodes(node.List, item.value)
		if partErr != nil {
			return nil, partErr
		}
		segments = append(segments, part...)
	}
	return segments, nil
}

func (e templateEvaluator) clone() templateEvaluator {
	variables := make(map[string]any, len(e.variables))
	maps.Copy(variables, e.variables)
	e.variables = variables
	return e
}

func (e templateEvaluator) pipeline(pipe *parse.PipeNode, dot any) (any, error) {
	var value any
	for index, command := range pipe.Cmds {
		result, err := e.command(command, dot, value, index > 0)
		if err != nil {
			return nil, err
		}
		value = result
		if _, page := value.(pageValue); page && index < len(pipe.Cmds)-1 {
			return nil, errors.New("page fields must be emitted directly and cannot be piped to a helper")
		}
	}
	return value, nil
}

func (e templateEvaluator) command(command *parse.CommandNode, dot, piped any, hasPiped bool) (any, error) {
	if len(command.Args) == 0 {
		return nil, errors.New("empty template command")
	}
	if identifier, ok := command.Args[0].(*parse.IdentifierNode); ok {
		arguments := make([]any, 0, len(command.Args))
		for _, argument := range command.Args[1:] {
			value, err := e.argument(argument, dot)
			if err != nil {
				return nil, err
			}
			arguments = append(arguments, value)
		}
		if hasPiped {
			arguments = append(arguments, piped)
		}
		return e.call(identifier.Ident, arguments)
	}
	if len(command.Args) != 1 || hasPiped {
		return nil, errors.New("template command requires a supported function")
	}
	return e.argument(command.Args[0], dot)
}

func (e templateEvaluator) argument(node parse.Node, dot any) (any, error) {
	switch current := node.(type) {
	case *parse.DotNode:
		return dot, nil
	case *parse.StringNode:
		return current.Text, nil
	case *parse.BoolNode:
		return current.True, nil
	case *parse.NilNode:
		return nil, nil
	case *parse.NumberNode:
		if current.IsInt || current.IsUint || current.IsFloat {
			return json.Number(current.Text), nil
		}
		return nil, fmt.Errorf("number %q is unsupported", current.Text)
	case *parse.FieldNode:
		return lookupSegments(dot, current.Ident)
	case *parse.VariableNode:
		value, ok := e.variables[current.Ident[0]]
		if !ok {
			return nil, fmt.Errorf("variable %s is not defined", current.Ident[0])
		}
		return lookupSegments(value, current.Ident[1:])
	case *parse.ChainNode:
		value, err := e.argument(current.Node, dot)
		if err != nil {
			return nil, err
		}
		return lookupSegments(value, current.Field)
	case *parse.PipeNode:
		return e.pipeline(current, dot)
	default:
		return nil, fmt.Errorf("template argument %T is not supported", node)
	}
}

func (e templateEvaluator) call(name string, arguments []any) (any, error) {
	switch name {
	case "field":
		path, err := oneString(name, arguments)
		if err != nil {
			return nil, err
		}
		value, found := lookupPath(e.context.Fields, path)
		if !found || value == nil {
			return nil, fmt.Errorf("required field %q is missing", path)
		}
		return value, nil
	case "optional":
		path, err := oneString(name, arguments)
		if err != nil {
			return nil, err
		}
		value, found := lookupPath(e.context.Fields, path)
		if !found || value == nil {
			return nil, nil
		}
		return value, nil
	case "mdoc":
		path, err := oneString(name, arguments)
		if err != nil {
			return nil, err
		}
		value, found := lookupPath(e.context.Computed, path)
		if !found || value == nil {
			return nil, fmt.Errorf("computed field %q is missing", path)
		}
		return value, nil
	case "page":
		path, err := oneString(name, arguments)
		if err != nil {
			return nil, err
		}
		if !e.context.AllowPageFields {
			return nil, fmt.Errorf("page field %q is only supported in headers and footers", path)
		}
		switch path {
		case "number":
			return pageValue(SegmentPage), nil
		case "count":
			return pageValue(SegmentPageCount), nil
		default:
			return nil, fmt.Errorf("page field %q is unsupported", path)
		}
	case "upper", "lower":
		if len(arguments) != 1 {
			return nil, fmt.Errorf("%s requires one value", name)
		}
		text, err := scalarString(arguments[0])
		if err != nil {
			return nil, err
		}
		if name == "upper" {
			return strings.ToUpper(text), nil
		}
		return strings.ToLower(text), nil
	case "join":
		if len(arguments) != 2 {
			return nil, errors.New("join requires a separator and a list")
		}
		separator, ok := arguments[0].(string)
		if !ok {
			return nil, errors.New("join separator must be text")
		}
		list, ok := arguments[1].([]any)
		if !ok {
			return nil, errors.New("join value must be a list")
		}
		parts := make([]string, len(list))
		for index := range list {
			part, err := scalarString(list[index])
			if err != nil {
				return nil, err
			}
			parts[index] = part
		}
		return strings.Join(parts, separator), nil
	case "date":
		return formatDate(arguments)
	case "number":
		return formatNumber(arguments)
	case "eq", "ne":
		if len(arguments) != 2 {
			return nil, fmt.Errorf("%s requires two values", name)
		}
		equal := equalValues(arguments[0], arguments[1])
		if name == "ne" {
			equal = !equal
		}
		return equal, nil
	case "and":
		for _, argument := range arguments {
			if !truth(argument) {
				return false, nil
			}
		}
		return true, nil
	case "or":
		if slices.ContainsFunc(arguments, truth) {
			return true, nil
		}
		return false, nil
	case "not":
		if len(arguments) != 1 {
			return nil, errors.New("not requires one value")
		}
		return !truth(arguments[0]), nil
	case "len":
		if len(arguments) != 1 {
			return nil, errors.New("len requires one value")
		}
		value := reflect.ValueOf(arguments[0])
		if !value.IsValid() {
			return int64(0), nil
		}
		switch value.Kind() {
		case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
			return int64(value.Len()), nil
		default:
			return nil, errors.New("len requires text, a list, or a map")
		}
	default:
		return nil, fmt.Errorf("template function %q is not available", name)
	}
}

func valueSegment(value any) ([]Segment, error) {
	if value == nil {
		return nil, nil
	}
	if page, ok := value.(pageValue); ok {
		return []Segment{{Kind: SegmentKind(page)}}, nil
	}
	text, err := printable(value)
	if err != nil {
		return nil, err
	}
	return []Segment{{Kind: SegmentText, Text: text}}, nil
}

func printable(value any) (string, error) {
	if text, err := scalarString(value); err == nil {
		return text, nil
	}
	data, err := CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func scalarString(value any) (string, error) {
	switch current := value.(type) {
	case nil:
		return "", nil
	case string:
		return current, nil
	case bool:
		return strconv.FormatBool(current), nil
	case json.Number:
		return current.String(), nil
	case int:
		return strconv.Itoa(current), nil
	case int64:
		return strconv.FormatInt(current, 10), nil
	case uint64:
		return strconv.FormatUint(current, 10), nil
	case float64:
		return strconv.FormatFloat(current, 'g', -1, 64), nil
	case pageValue:
		return "", errors.New("page fields cannot be passed to text formatting helpers")
	default:
		return "", fmt.Errorf("value of type %T is not scalar", value)
	}
}

func lookupPath(root map[string]any, path string) (any, bool) {
	if path == "" {
		return root, true
	}
	return lookupSegmentsFound(root, strings.Split(path, "."))
}

func lookupSegments(value any, segments []string) (any, error) {
	result, found := lookupSegmentsFound(value, segments)
	if !found {
		return nil, fmt.Errorf("value %q is missing", strings.Join(segments, "."))
	}
	return result, nil
}

func lookupSegmentsFound(value any, segments []string) (any, bool) {
	current := value
	for _, segment := range segments {
		mapping, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = mapping[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func oneString(name string, arguments []any) (string, error) {
	if len(arguments) != 1 {
		return "", fmt.Errorf("%s requires one path", name)
	}
	value, ok := arguments[0].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("%s path must be non-empty text", name)
	}
	return value, nil
}

func formatDate(arguments []any) (string, error) {
	layout := "2006-01-02"
	var value any
	switch len(arguments) {
	case 1:
		value = arguments[0]
	case 2:
		var ok bool
		layout, ok = arguments[0].(string)
		if !ok || layout == "" {
			return "", errors.New("date layout must be text")
		}
		value = arguments[1]
	default:
		return "", errors.New("date requires a value and optional layout")
	}
	text, err := scalarString(value)
	if err != nil {
		return "", err
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		parsed, err = time.Parse("2006-01-02", text)
	}
	if err != nil {
		return "", fmt.Errorf("date value %q must use YYYY-MM-DD or RFC3339", text)
	}
	return parsed.Format(layout), nil
}

func formatNumber(arguments []any) (string, error) {
	decimals := -1
	var value any
	switch len(arguments) {
	case 1:
		value = arguments[0]
	case 2:
		parsed, ok := toInt(arguments[0])
		if !ok || parsed < 0 || parsed > 12 {
			return "", errors.New("number decimals must be between 0 and 12")
		}
		decimals = parsed
		value = arguments[1]
	default:
		return "", errors.New("number requires a value and optional decimal count")
	}
	text, err := scalarString(value)
	if err != nil {
		return "", err
	}
	number, ok := parseExactNumber(text)
	if !ok {
		return "", fmt.Errorf("number value %q is invalid", text)
	}
	if decimals >= 0 {
		return number.FloatString(decimals), nil
	}
	formatted, ok := exactDecimalString(number)
	if !ok {
		return "", fmt.Errorf("number value %q cannot be represented as a finite decimal", text)
	}
	return formatted, nil
}

func toInt(value any) (int, bool) {
	switch current := value.(type) {
	case int64:
		return int(current), true
	case uint64:
		return int(current), true
	case json.Number:
		parsed, err := strconv.Atoi(current.String())
		return parsed, err == nil
	default:
		return 0, false
	}
}

func truth(value any) bool {
	if value == nil {
		return false
	}
	if number, ok := value.(json.Number); ok {
		parsed, valid := numericValue(number)
		return valid && parsed.Sign() != 0
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Bool:
		return reflected.Bool()
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return reflected.Len() > 0
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int() != 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflected.Uint() != 0
	case reflect.Float32, reflect.Float64:
		return reflected.Float() != 0
	default:
		return true
	}
}

func equalValues(left, right any) bool {
	leftNumber, leftOK := numericValue(left)
	rightNumber, rightOK := numericValue(right)
	if leftOK && rightOK {
		return leftNumber.Cmp(rightNumber) == 0
	}
	return reflect.DeepEqual(left, right)
}

func numericValue(value any) (*big.Rat, bool) {
	var text string
	switch current := value.(type) {
	case json.Number:
		text = current.String()
	case int:
		text = strconv.Itoa(current)
	case int64:
		text = strconv.FormatInt(current, 10)
	case uint64:
		text = strconv.FormatUint(current, 10)
	case float64:
		text = strconv.FormatFloat(current, 'g', -1, 64)
	default:
		return nil, false
	}
	return parseExactNumber(text)
}

func parseExactNumber(text string) (*big.Rat, bool) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, false
	}
	if _, ok := decoded.(json.Number); !ok {
		return nil, false
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, false
	}
	parsed, ok := new(big.Rat).SetString(text)
	return parsed, ok
}

func exactDecimalString(value *big.Rat) (string, bool) {
	denominator := new(big.Int).Set(value.Denom())
	twos := 0
	fives := 0
	two := big.NewInt(2)
	five := big.NewInt(5)
	remainder := new(big.Int)
	for {
		quotient, mod := new(big.Int).QuoRem(denominator, two, remainder)
		if mod.Sign() != 0 {
			break
		}
		denominator = quotient
		twos++
	}
	for {
		quotient, mod := new(big.Int).QuoRem(denominator, five, remainder)
		if mod.Sign() != 0 {
			break
		}
		denominator = quotient
		fives++
	}
	if denominator.Cmp(big.NewInt(1)) != 0 {
		return "", false
	}
	decimals := max(fives, twos)
	formatted := value.FloatString(decimals)
	if strings.Contains(formatted, ".") {
		formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
	}
	if formatted == "-0" {
		formatted = "0"
	}
	return formatted, true
}

type rangeItem struct {
	key   any
	value any
}

func rangeItems(value any) ([]rangeItem, error) {
	switch current := value.(type) {
	case []any:
		items := make([]rangeItem, len(current))
		for index := range current {
			items[index] = rangeItem{key: int64(index), value: current[index]}
		}
		return items, nil
	case map[string]any:
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		items := make([]rangeItem, len(keys))
		for index, key := range keys {
			items[index] = rangeItem{key: key, value: current[key]}
		}
		return items, nil
	default:
		return nil, fmt.Errorf("range requires a list or map, got %T", value)
	}
}

func compactSegments(input []Segment) []Segment {
	result := make([]Segment, 0, len(input))
	for _, segment := range input {
		if segment.Kind == SegmentText && segment.Text == "" {
			continue
		}
		if segment.Kind == SegmentText && len(result) > 0 && result[len(result)-1].Kind == SegmentText {
			result[len(result)-1].Text += segment.Text
			continue
		}
		result = append(result, segment)
	}
	return result
}
