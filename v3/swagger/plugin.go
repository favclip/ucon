package swagger

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/favclip/ucon/v3"
)

// interface compatibility check
var _ ucon.HandlersScannerPlugin = &Plugin{}
var _ ucon.Context = &HandlerInfo{}
var _ ucon.HandlerContainer = &HandlerInfo{}

type swaggerOperationKey struct{}

var httpReqType = reflect.TypeOf(&http.Request{})
var httpRespType = reflect.TypeOf((*http.ResponseWriter)(nil)).Elem()
var netContextType = reflect.TypeOf((*context.Context)(nil)).Elem()
var errorType = reflect.TypeOf((*error)(nil)).Elem()
var uconHTTPErrorType = reflect.TypeOf((*ucon.HTTPErrorResponse)(nil)).Elem()

// DefaultTypeSchemaMapper is used for mapping from go-type to swagger-schema.
var DefaultTypeSchemaMapper = map[reflect.Type]*TypeSchema{
	reflect.TypeOf(time.Time{}): &TypeSchema{
		RefName: "",
		Schema: &Schema{
			Type:   "string",
			Format: "date-time", // RFC3339
		},
		AllowRef: false,
	},
}

// 備忘
// swaggerのJSONを組み上げる上で、色々なTypeを走査せねばならない。
// トップレベルはもちろんTypeからなんだが、Typeの構成要素はTypeだけではない。
// 1つのTypeは built-in type だったり struct する。
// structの場合、要素は 名前 + Type + タグ であり、Typeだけから1つのパーツがなっているわけではない。

// FieldInfo has a information for struct field.
// It contains Type information and Tag information.
type FieldInfo struct {
	Base reflect.StructField

	TypeSchema   *TypeSchema
	EmitAsString bool
	Enum         []interface{} // from tag, e.g. swagger:",enum=ok|ng"
}

// Anonymous is an embedded field.
func (fiInfo *FieldInfo) Anonymous() bool {
	return fiInfo.Base.Anonymous
}

// Type returns field type.
func (fiInfo *FieldInfo) Type() reflect.Type {
	return fiInfo.Base.Type
}

// Ignored is an ignored field.
func (fiInfo *FieldInfo) Ignored() bool {
	tagJSON := ucon.NewTagJSON(fiInfo.Base.Tag)
	if tagJSON.Ignored() {
		return true
	}

	return false
}

// Name returns field name on swagger.
func (fiInfo *FieldInfo) Name() string {
	tagJSON := ucon.NewTagJSON(fiInfo.Base.Tag)
	name := tagJSON.Name()
	if name == "" {
		name = fiInfo.Base.Name
	}

	return name
}

// TypeSchema is a container of swagger schema and its attributes.
// RefName must be given if AllowRef is true.
type TypeSchema struct {
	RefName  string
	Schema   *Schema
	AllowRef bool
}

// SwaggerSchema returns schema that is can use is swagger.json.
func (ts *TypeSchema) SwaggerSchema() (*Schema, error) {
	if ts.AllowRef && ts.RefName != "" {
		return &Schema{Ref: fmt.Sprintf("#/definitions/%s", ts.RefName)}, nil
	} else if ts.AllowRef {
		return nil, errors.New("Name is required")
	}

	return ts.Schema.ShallowCopy(), nil
}

// Plugin is a holder for all of plugin settings.
type Plugin struct {
	constructor *swaggerObjectConstructor
	options     *Options
}

type swaggerObjectConstructor struct {
	plugin           *Plugin
	object           *Object
	typeSchemaMapper map[reflect.Type]*TypeSchema

	finisher []func() error
}

type parameterWrapper struct {
	StructField reflect.StructField
}

// HandlerInfo is a container of the handler function and the operation with the context.
// HandlerInfo implements interfaces of ucon.HandlerContainer and ucon.Context.
type HandlerInfo struct {
	HandlerFunc interface{}
	Operation
	Context ucon.Context
}

// Options is a container of optional settings to configure a plugin.
type Options struct {
	SwaggerJSONPath        string
	Object                 *Object
	DefinitionNameModifier func(refT reflect.Type, defName string) string
	IgnoreRoute            func(rd *ucon.RouteDefinition) bool
}

// NewPlugin returns new swagger plugin configured with the options.
func NewPlugin(opts *Options) *Plugin {
	if opts == nil {
		opts = &Options{}
	}

	if opts.SwaggerJSONPath == "" {
		opts.SwaggerJSONPath = "/api/swagger.json"
	}

	so := opts.Object

	if so == nil {
		so = &Object{}
	}
	if so.Paths == nil {
		so.Paths = make(Paths, 0)
	}
	if so.Definitions == nil {
		so.Definitions = make(Definitions, 0)
	}

	p := &Plugin{options: opts}
	soConstructor := &swaggerObjectConstructor{
		plugin:           p,
		object:           so,
		typeSchemaMapper: make(map[reflect.Type]*TypeSchema),
	}
	for k, v := range DefaultTypeSchemaMapper {
		soConstructor.typeSchemaMapper[k] = v
	}
	p.constructor = soConstructor

	return p
}

// HandlersScannerProcess executes scanning all registered handlers to serve swagger.json.
func (p *Plugin) HandlersScannerProcess(m *ucon.ServeMux, rds []*ucon.RouteDefinition) error {
	soConstructor := p.constructor

	// construct swagger.json
	for _, rd := range rds {
		err := soConstructor.processHandler(rd)
		if err != nil {
			return err
		}
	}

	err := soConstructor.object.finish()
	if err != nil {
		return err
	}

	// supply swagger.json endpoint
	m.HandleFunc("GET", p.options.SwaggerJSONPath, func(w http.ResponseWriter, r *http.Request) *Object {
		return soConstructor.object
	})

	return nil
}

func (soConstructor *swaggerObjectConstructor) processHandler(rd *ucon.RouteDefinition) error {
	item := soConstructor.object.Paths[rd.PathTemplate.PathTemplate]
	if item == nil {
		item = &PathItem{}
	}

	if soConstructor.plugin.options.IgnoreRoute != nil && soConstructor.plugin.options.IgnoreRoute(rd) {
		return nil
	}

	var setOperation func(op *Operation)
	switch rd.Method {
	case "GET":
		setOperation = func(op *Operation) {
			item.Get = op
		}
	case "PUT":
		setOperation = func(op *Operation) {
			item.Put = op
		}
	case "POST":
		setOperation = func(op *Operation) {
			item.Post = op
		}
	case "DELETE":
		setOperation = func(op *Operation) {
			item.Delete = op
		}
	case "OPTIONS":
		setOperation = func(op *Operation) {
			item.Options = op
		}
	case "HEAD":
		setOperation = func(op *Operation) {
			item.Head = op
		}
	case "PATCH":
		setOperation = func(op *Operation) {
			item.Patch = op
		}
	case "*":
		// swagger.json should skip wildcard method
		return nil
	default:
		return fmt.Errorf("unknown method: %s", rd.Method)
	}

	if op, err := soConstructor.extractSwaggerOperation(rd); err != nil {
		soConstructor.finisher = nil
		return err
	} else if op != nil {
		setOperation(op)
		soConstructor.object.Paths[rd.PathTemplate.PathTemplate] = item

		err := soConstructor.execFinisher()
		if err != nil {
			return err
		}

		for _, ts := range soConstructor.typeSchemaMapper {
			if !ts.AllowRef {
				continue
			}
			if ts.RefName == "" {
				return errors.New("Name is required")
			}

			if _, ok := soConstructor.object.Definitions[ts.RefName]; !ok {
				soConstructor.object.Definitions[ts.RefName] = ts.Schema
			}
		}
	}

	return nil
}

func (soConstructor *swaggerObjectConstructor) extractSwaggerOperation(rd *ucon.RouteDefinition) (*Operation, error) {
	var op *Operation
	op, ok := rd.HandlerContainer.Value(swaggerOperationKey{}).(*Operation)
	if !ok || op == nil {
		op = &Operation{
			Description: fmt.Sprintf("%s %s", rd.Method, rd.PathTemplate.PathTemplate),
		}
	}
	if len(op.Responses) == 0 {
		op.Responses = make(Responses, 0)
		op.Responses["200"] = &Response{
			Description: fmt.Sprintf("response of %s %s", rd.Method, rd.PathTemplate.PathTemplate),
		}
	}

	var reqType, respType, errType reflect.Type
	handlerT := reflect.TypeOf(rd.HandlerContainer.Handler())
	for i, numIn := 0, handlerT.NumIn(); i < numIn; i++ {
		arg := handlerT.In(i)
		if arg == httpReqType {
			continue
		} else if arg == httpRespType {
			continue
		} else if arg == netContextType {
			continue
		}
		reqType = arg
		break
	}
	for i, numOut := 0, handlerT.NumOut(); i < numOut; i++ {
		ret := handlerT.Out(i)
		if ret.AssignableTo(errorType) {
			errType = ret
			continue
		}
		respType = ret
	}
	if respType == nil && errType == nil {
		// static file handler...?
		return nil, nil
	}

	// parameter
	var bodyParameter *Parameter
	if reqType != nil {
		paramNames, paramMap, err := soConstructor.extractParameterMapperMap(reqType)
		if err != nil {
			return nil, err
		}
		sort.Strings(paramNames)

		needBody := false
	outer:
		for _, paramName := range paramNames {
			pw := paramMap[paramName]

			// in path
			if pw.InPath() {
				op.Parameters = append(op.Parameters, &Parameter{
					Name:      paramName,
					In:        "path",
					Required:  true,
					Type:      pw.ParameterType(),
					Format:    pw.ParameterFormat(),
					Enum:      pw.ParameterEnum(),
					Minimum:   pw.Minimum(),
					Maximum:   pw.Maximum(),
					MinLength: pw.MinLength(),
					MaxLength: pw.MaxLength(),
					Pattern:   pw.Pattern(),
				})

				continue
			} else {
				for _, pathParam := range rd.PathTemplate.PathParameters {
					if paramName != pathParam {
						continue
					}
					op.Parameters = append(op.Parameters, &Parameter{
						Name:      paramName,
						In:        "path",
						Required:  true,
						Type:      pw.ParameterType(),
						Format:    pw.ParameterFormat(),
						Enum:      pw.ParameterEnum(),
						Minimum:   pw.Minimum(),
						Maximum:   pw.Maximum(),
						MinLength: pw.MinLength(),
						MaxLength: pw.MaxLength(),
						Pattern:   pw.Pattern(),
					})
					continue outer
				}
			}

			// in query
			if pw.InQuery() {
				param := &Parameter{
					Name:      pw.Name(),
					In:        "query",
					Required:  pw.Required(),
					Type:      pw.ParameterType(),
					Format:    pw.ParameterFormat(),
					Minimum:   pw.Minimum(),
					Maximum:   pw.Maximum(),
					MinLength: pw.MinLength(),
					MaxLength: pw.MaxLength(),
					Pattern:   pw.Pattern(),
				}
				if param.Type == "array" {
					fiInfo, err := soConstructor.extractFieldInfo(pw.StructField)
					if err != nil {
						return nil, err
					}
					soConstructor.addFinisher(func() error {
						ts := fiInfo.TypeSchema

						// NOTE(laco) Parameter.Items doesn't allow `$ref`.
						// Parameter.Items.Type is required.
						if ts.Schema == nil || ts.Schema.Items == nil || ts.Schema.Items.Type == "" {
							return errors.New("Items is required")
						}
						param.Items = &Items{}
						param.Items.Type = ts.Schema.Items.Type
						param.Items.Format = ts.Schema.Items.Format
						if fiInfo.EmitAsString {
							param.Items.Type = "string"
						}
						param.Items.Enum = fiInfo.Enum

						return nil
					})
				} else {
					param.Enum = pw.ParameterEnum()
				}

				op.Parameters = append(op.Parameters, param)

				continue
			}

			if pw.Private() {
				continue
			}

			needBody = true
		}

		// in body
		if needBody {
			bodyParameter = &Parameter{
				Name:     "body",
				In:       "body",
				Required: true,
				Schema:   nil,
			}

			switch rd.Method {
			case "GET", "DELETE":
				bodyParameter.Required = false
			}
			op.Parameters = append(op.Parameters, bodyParameter)
		}
	}

	if reqType != nil && bodyParameter != nil {
		ts, err := soConstructor.extractTypeSchema(reqType)
		if err != nil {
			return nil, err
		}
		if bodyParameter != nil {
			soConstructor.addFinisher(func() error {
				schema, err := ts.SwaggerSchema()
				if err != nil {
					return err
				}
				bodyParameter.Schema = schema

				return nil
			})
		}
	}

	if respType != nil {
		ts, err := soConstructor.extractTypeSchema(respType)
		if err != nil {
			return nil, err
		}

		soConstructor.addFinisher(func() error {
			for _, resp := range op.Responses {
				schema, err := ts.SwaggerSchema()
				if err != nil {
					return err
				}
				resp.Schema = schema
			}

			return nil
		})
	}

	if errType != nil {
		if errType == errorType {
			// pass
		} else if errType == uconHTTPErrorType {
			// pass
		} else {
			ts, err := soConstructor.extractTypeSchema(errType)
			if err != nil {
				return nil, err
			}

			soConstructor.addFinisher(func() error {
				if op.Responses["default"] == nil {
					resp := &Response{
						Description: "???", // TODO
					}
					op.Responses["default"] = resp

					schema, err := ts.SwaggerSchema()
					if err != nil {
						return err
					}
					resp.Schema = schema
				}

				return nil
			})
		}
	}

	return op, nil
}

func (soConstructor *swaggerObjectConstructor) extractFieldInfo(sf reflect.StructField) (*FieldInfo, error) {
	fiInfo := &FieldInfo{Base: sf}

	if fiInfo.Ignored() {
		return fiInfo, nil
	}

	ts, err := soConstructor.extractTypeSchema(sf.Type)
	if err != nil {
		return nil, err
	}
	fiInfo.TypeSchema = ts

	fiInfo.EmitAsString = ucon.NewTagJSON(sf.Tag).HasString()
	enumAsString := NewTagSwagger(sf.Tag).Enum()
	var enum []interface{}
	switch sf.Type.Kind() {
	case reflect.Struct:
	case reflect.Slice, reflect.Array:
	case reflect.Bool:
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32:
		for _, enumStr := range enumAsString {
			v, err := strconv.ParseInt(enumStr, 0, 32)
			if err != nil {
				return nil, err
			}
			enum = append(enum, int32(v))
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		for _, enumStr := range enumAsString {
			v, err := strconv.ParseUint(enumStr, 0, 32)
			if err != nil {
				return nil, err
			}
			enum = append(enum, uint32(v))
		}
	case reflect.Int64:
		for _, enumStr := range enumAsString {
			v, err := strconv.ParseInt(enumStr, 0, 64)
			if err != nil {
				return nil, err
			}
			enum = append(enum, v)
		}
	case reflect.Uint64:
		for _, enumStr := range enumAsString {
			v, err := strconv.ParseUint(enumStr, 0, 64)
			if err != nil {
				return nil, err
			}
			enum = append(enum, v)
		}
	case reflect.Float32:
		for _, enumStr := range enumAsString {
			v, err := strconv.ParseFloat(enumStr, 32)
			if err != nil {
				return nil, err
			}
			enum = append(enum, float32(v))
		}
	case reflect.Float64:
		for _, enumStr := range enumAsString {
			v, err := strconv.ParseFloat(enumStr, 64)
			if err != nil {
				return nil, err
			}
			enum = append(enum, v)
		}
	case reflect.String:
		for _, enumStr := range enumAsString {
			enum = append(enum, enumStr)
		}
	default:
	}

	if fiInfo.EmitAsString {
		// value format compatibility check was done in above code
		enum = nil
		for _, enumStr := range enumAsString {
			enum = append(enum, enumStr)
		}
	}
	fiInfo.Enum = enum

	return fiInfo, nil
}

func (soConstructor *swaggerObjectConstructor) extractTypeSchema(refT reflect.Type) (*TypeSchema, error) {
	if refT.Kind() == reflect.Ptr {
		refT = refT.Elem()
	}

	if ts, ok := soConstructor.typeSchemaMapper[refT]; ok {
		return ts, nil
	}

	ts := &TypeSchema{}
	soConstructor.typeSchemaMapper[refT] = ts

	schema := &Schema{}
	schema.Type, schema.Format = extractSwaggerTypeAndFormat(refT)

	ts.Schema = schema

	defName := refT.Name()
	if soConstructor.plugin.options.DefinitionNameModifier != nil {
		defName = soConstructor.plugin.options.DefinitionNameModifier(refT, defName)
	}
	ts.RefName = defName

	if defName != "" && refT.PkgPath() != "" {
		// reject builtin-type, aka int, bool, string
		ts.AllowRef = true
	}

	switch schema.Type {
	case "object":
		var process func(refT reflect.Type) error
		process = func(refT reflect.Type) error {
			if refT.Kind() == reflect.Ptr {
				refT = refT.Elem()
			}
			if refT.Kind() != reflect.Struct {
				return nil
			}
			for i, numField := 0, refT.NumField(); i < numField; i++ {
				sf := refT.Field(i)

				fiInfo, err := soConstructor.extractFieldInfo(sf)
				if err != nil {
					return err
				}

				if fiInfo.Ignored() {
					continue
				}

				if fiInfo.Anonymous() {
					// it just means same struct.
					err := process(sf.Type)
					if err != nil {
						return err
					}
					continue
				}

				soConstructor.addFinisher(func() error {
					fiSchema, err := fiInfo.TypeSchema.SwaggerSchema()
					if err != nil {
						return err
					}
					if fiInfo.EmitAsString {
						fiSchema.Type = "string"
					}

					tagSwagger := NewTagSwagger(fiInfo.Base.Tag)

					if tagSwagger.Required() {
						schema.Required = append(schema.Required, fiInfo.Name())
					}
					switch fiSchema.Type {
					case "string":
						fiSchema.Pattern = tagSwagger.Pattern()
						fiSchema.MinLength = tagSwagger.MinLength()
						fiSchema.MaxLength = tagSwagger.MaxLength()
					case "integer", "number":
						fiSchema.Minimum = tagSwagger.Minimum()
						fiSchema.Maximum = tagSwagger.Maximum()
					}

					fiSchema.Enum = fiInfo.Enum
					schema.Properties[fiInfo.Name()] = fiSchema

					return nil
				})
			}
			return nil
		}

		if schema.Properties == nil {
			schema.Properties = make(map[string]*Schema, 0)
		}
		err := process(refT)
		if err != nil {
			return nil, err
		}

	case "array":
		{
			var ts *TypeSchema
			var err error
			soConstructor.addFinisher(func() error {
				itemSchema, err := ts.SwaggerSchema()
				if err != nil {
					return err
				}
				schema.Items = itemSchema

				return nil
			})
			ts, err = soConstructor.extractTypeSchema(refT.Elem())
			if err != nil {
				return nil, err
			}
		}

	case "":
		return nil, fmt.Errorf("unknown schema type: %s", refT.Kind().String())
	default:
	}

	return ts, nil
}

func (soConstructor *swaggerObjectConstructor) extractParameterMapperMap(refT reflect.Type) ([]string, map[string]*parameterWrapper, error) {
	paramNames := make([]string, 0)
	parameterMap := make(map[string]*parameterWrapper, 0)

	var process func(refT reflect.Type) error
	process = func(refT reflect.Type) error {
		if refT.Kind() == reflect.Ptr {
			refT = refT.Elem()
		}

		for i, numField := 0, refT.NumField(); i < numField; i++ {
			sf := refT.Field(i)
			pw := &parameterWrapper{
				StructField: sf,
			}

			if pw.Private() {
				continue
			}

			if sf.Anonymous {
				err := process(sf.Type)
				if err != nil {
					return err
				}
				continue
			}

			name := pw.Name()
			if name == "" {
				name = sf.Name
			}

			paramNames = append(paramNames, name)
			parameterMap[name] = pw
		}
		return nil
	}

	err := process(refT)
	if err != nil {
		return nil, nil, err
	}

	return paramNames, parameterMap, nil
}

func (soConstructor *swaggerObjectConstructor) addFinisher(f func() error) {
	soConstructor.finisher = append(soConstructor.finisher, f)
}

func (soConstructor *swaggerObjectConstructor) execFinisher() error {
	for _, f := range soConstructor.finisher {
		err := f()
		if err != nil {
			return err
		}
	}
	soConstructor.finisher = nil

	return nil
}

func extractSwaggerTypeAndFormat(refT reflect.Type) (string, string) {
	if refT.Kind() == reflect.Ptr {
		refT = refT.Elem()
	}

	var t string
	var f string
	switch refT.Kind() {
	case reflect.Struct:
		t = "object"
	case reflect.Slice, reflect.Array:
		t = "array"
	case reflect.Bool:
		t = "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		t = "integer"
		f = "int32"
	case reflect.Int64, reflect.Uint64:
		t = "integer"
		f = "int64"
	case reflect.Float32:
		t = "number"
		f = "float"
	case reflect.Float64:
		t = "number"
		f = "double"
	case reflect.String:
		t = "string"
	default:
		t = ""
	}

	return t, f
}

// AddTag adds the tag to top-level tags definition.
func (p *Plugin) AddTag(tag *Tag) *Tag {
	p.constructor.object.Tags = append(p.constructor.object.Tags, tag)

	return tag
}

func (pw *parameterWrapper) ParameterType() string {
	if ucon.NewTagJSON(pw.StructField.Tag).HasString() {
		return "string"
	}
	t, _ := extractSwaggerTypeAndFormat(pw.StructField.Type)
	return t
}

func (pw *parameterWrapper) ParameterFormat() string {
	refT := pw.StructField.Type

	if refT.Kind() == reflect.Ptr {
		refT = refT.Elem()
	}

	switch refT.Kind() {
	case reflect.Int64:
		return "int64"
	default:
		return ""
	}
}

func (pw *parameterWrapper) ParameterEnum() []interface{} {
	enumStrs := pw.Enum()
	vs := make([]interface{}, 0, len(enumStrs))
	for _, str := range enumStrs {
		vs = append(vs, str)
	}
	return vs
}

func (pw *parameterWrapper) InPath() bool {
	swaggerTag := NewTagSwagger(pw.StructField.Tag)
	return swaggerTag.In() == "path"
}

func (pw *parameterWrapper) InQuery() bool {
	swaggerTag := NewTagSwagger(pw.StructField.Tag)
	return swaggerTag.In() == "query"
}

func (pw *parameterWrapper) Name() string {
	swaggerTag := NewTagSwagger(pw.StructField.Tag)
	name := swaggerTag.Name()
	if name != "" {
		return name
	}

	jsonTag := ucon.NewTagJSON(pw.StructField.Tag)
	name = jsonTag.Name()
	if name != "" {
		return name
	}

	return pw.StructField.Name
}

func (pw *parameterWrapper) Required() bool {
	swaggerTag := NewTagSwagger(pw.StructField.Tag)
	return swaggerTag.Required()
}

func (pw *parameterWrapper) Enum() []string {
	swaggerTag := NewTagSwagger(pw.StructField.Tag)
	return swaggerTag.Enum()
}

func (pw *parameterWrapper) Private() bool {
	swaggerTag := NewTagSwagger(pw.StructField.Tag)
	if swaggerTag.Private() {
		return true
	}

	jsonTag := ucon.NewTagJSON(pw.StructField.Tag)
	if jsonTag.Ignored() {
		return true
	}

	return false
}

func (pw *parameterWrapper) Maximum() *int {
	swaggerTag := NewTagSwagger(pw.StructField.Tag)
	return swaggerTag.Maximum()
}

func (pw *parameterWrapper) Minimum() *int {
	swaggerTag := NewTagSwagger(pw.StructField.Tag)
	return swaggerTag.Minimum()
}

func (pw *parameterWrapper) MaxLength() *int {
	swaggerTag := NewTagSwagger(pw.StructField.Tag)
	return swaggerTag.MaxLength()
}

func (pw *parameterWrapper) MinLength() *int {
	swaggerTag := NewTagSwagger(pw.StructField.Tag)
	return swaggerTag.MinLength()
}

func (pw *parameterWrapper) Pattern() string {
	swaggerTag := NewTagSwagger(pw.StructField.Tag)
	return swaggerTag.Pattern()
}

// NewHandlerInfo returns new HandlerInfo containing given handler function.
func NewHandlerInfo(handler interface{}) *HandlerInfo {
	ucon.CheckFunction(handler)
	return &HandlerInfo{
		HandlerFunc: handler,
	}
}

// Handler returns contained handler function.
func (wr *HandlerInfo) Handler() interface{} {
	return wr.HandlerFunc
}

// Value returns the value contained with the key.
func (wr *HandlerInfo) Value(key interface{}) interface{} {
	if key == (swaggerOperationKey{}) {
		return &wr.Operation
	}
	if wr.Context != nil {
		return wr.Context.Value(key)
	}
	return nil
}
