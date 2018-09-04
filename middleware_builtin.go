package ucon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"reflect"
	"strings"

	"github.com/favclip/golidator"
)

var httpReqType = reflect.TypeOf((*http.Request)(nil))
var httpRespType = reflect.TypeOf((*http.ResponseWriter)(nil)).Elem()
var contextType = reflect.TypeOf((*context.Context)(nil)).Elem()
var errorType = reflect.TypeOf((*error)(nil)).Elem()
var stringParserType = reflect.TypeOf((*StringParser)(nil)).Elem()

// PathParameterKey is context key of path parameter. context returns map[string]string.
var PathParameterKey = &struct{ temp string }{}

// ErrInvalidPathParameterType is the error that context with PathParameterKey key returns not map[string]string type.
var ErrInvalidPathParameterType = errors.New("path parameter type should be map[string]string")

// ErrPathParameterFieldMissing is the path parameter mapping error.
var ErrPathParameterFieldMissing = errors.New("can't find path parameter in struct")

// ErrCSRFBadToken is the error returns CSRF token verify failure.
var ErrCSRFBadToken = newBadRequestf("invalid CSRF token")

// HTTPErrorResponse is a response to represent http errors.
type HTTPErrorResponse interface {
	// StatusCode returns http response status code.
	StatusCode() int
	// ErrorMessage returns an error object.
	// Returned object will be converted by json.Marshal and written as http response body.
	ErrorMessage() interface{}
}

// HTTPResponseModifier is an interface to hook on each responses and modify those.
// The hook will hijack ResponseMapper, so it makes possible to do something in place of ResponseMapper.
// e.g. You can convert a response object to xml and write it as response body.
type HTTPResponseModifier interface {
	Handle(b *Bubble) error
}

type httpError struct {
	Code    int         `json:"code"`
	Message interface{} `json:"message"`
}

func (he *httpError) StatusCode() int {
	return he.Code
}

func (he *httpError) ErrorMessage() interface{} {
	return he
}

func (he *httpError) Error() string {
	return fmt.Sprintf("status code %d: %s", he.StatusCode(), he.ErrorMessage())
}

func newBadRequestf(format string, a ...interface{}) *httpError {
	return &httpError{
		Code:    http.StatusBadRequest,
		Message: fmt.Sprintf(format, a...),
	}
}

// StringParser is a parser for string-to-object custom conversion.
type StringParser interface {
	ParseString(value string) (interface{}, error)
}

// HTTPRWDI injects Bubble.R and Bubble.W into the bubble.Arguments.
func HTTPRWDI() MiddlewareFunc {
	return func(b *Bubble) error {
		for idx, argT := range b.ArgumentTypes {
			if httpReqType.AssignableTo(argT) {
				b.Arguments[idx] = reflect.ValueOf(b.R)
				continue
			}
			if httpRespType.AssignableTo(argT) {
				b.Arguments[idx] = reflect.ValueOf(b.W)
				continue
			}
		}

		return b.Next()
	}
}

// NetContextDI injects Bubble.Context into the bubble.Arguments.
// deprecated. use ContextDI instead of NetContextDI.
func NetContextDI() MiddlewareFunc {
	return ContextDI()
}

// ContextDI injects Bubble.Context into the bubble.Arguments.
func ContextDI() MiddlewareFunc {
	return func(b *Bubble) error {
		for idx, argT := range b.ArgumentTypes {
			if contextType.AssignableTo(argT) {
				b.Arguments[idx] = reflect.ValueOf(b.Context)
				continue
			}
		}

		return b.Next()
	}
}

// RequestObjectMapper converts a request to object and injects it into the bubble.Arguments.
func RequestObjectMapper() MiddlewareFunc {
	return func(b *Bubble) error {
		argIdx := -1
		var argT reflect.Type
		for idx, arg := range b.Arguments {
			if arg.IsValid() {
				// already injected
				continue
			}
			if b.ArgumentTypes[idx].Kind() != reflect.Ptr || b.ArgumentTypes[idx].Elem().Kind() != reflect.Struct {
				// only support for struct
				continue
			}
			argT = b.ArgumentTypes[idx]
			argIdx = idx
			break
		}

		if argT == nil {
			return b.Next()
		}

		reqV := reflect.New(argT.Elem())
		req := reqV.Interface()

		// NOTE value will be overwritten by below process
		// url path extract
		if v := b.Context.Value(PathParameterKey); v != nil {
			params, ok := v.(map[string]string)
			if !ok {
				return ErrInvalidPathParameterType
			}
			for key, value := range params {
				found, _ := valueStringMapper(reqV, key, value)
				if !found {
					return ErrPathParameterFieldMissing
				}
			}
		}

		// url get parameter
		for key, ss := range b.R.URL.Query() {
			_, err := valueStringSliceMapper(reqV, key, ss)
			if err != nil {
				return err
			}
		}

		var body []byte
		var err error
		if b.R.Body != nil {
			// this case occured in unit test
			defer b.R.Body.Close()
			body, err = ioutil.ReadAll(b.R.Body)
		}
		if err != nil {
			return err
		}

		// request body as JSON
		{
			// where is the spec???
			ct := strings.Split(b.R.Header.Get("Content-Type"), ";")
			// TODO check charset
			if ct[0] == "application/json" {
				if len(body) == 2 {
					// dirty hack. {} map to []interface or [] map to normal struct.
				} else if len(body) != 0 {
					err := json.Unmarshal(body, req)
					if err != nil {
						return newBadRequestf(err.Error())
					}
				}
			}
		}
		// NOTE need request body as a=b&c=d style parsing?

		b.Arguments[argIdx] = reqV

		return b.Next()
	}
}

// ResponseMapper converts a response object to JSON and writes it as response body.
func ResponseMapper() MiddlewareFunc {
	return func(b *Bubble) error {
		err := b.Next()

		// first, error handling
		if err != nil {
			return b.writeErrorObject(err)
		}

		// second, error from handlers
		for idx := len(b.Returns) - 1; 0 <= idx; idx-- {
			rv := b.Returns[idx]
			if rv.Type().AssignableTo(errorType) && !rv.IsNil() {
				err := rv.Interface().(error)
				return b.writeErrorObject(err)
			}
		}

		// last, write payload
		for _, rv := range b.Returns {
			if rv.Type().AssignableTo(errorType) {
				continue
			}

			v := rv.Interface()
			if m, ok := v.(HTTPResponseModifier); ok {
				return m.Handle(b)
			} else if !rv.IsNil() {
				var resp []byte
				var err error
				if b.Debug {
					resp, err = json.MarshalIndent(v, "", "  ")
				} else {
					resp, err = json.Marshal(v)
				}
				if err != nil {
					http.Error(b.W, err.Error(), http.StatusInternalServerError)
					return err
				}
				b.W.Header().Set("Content-Type", "application/json; charset=UTF-8")
				b.W.WriteHeader(http.StatusOK)
				b.W.Write(resp)
				return nil
			} else {
				b.W.Header().Set("Content-Type", "application/json; charset=UTF-8")
				b.W.WriteHeader(http.StatusOK)
				if rv.Type().Kind() == reflect.Slice {
					b.W.Write([]byte("[]"))
				} else {
					b.W.Write([]byte("{}"))
				}
				return nil
			}
		}

		return nil
	}
}

func (b *Bubble) writeErrorObject(err error) error {
	he, ok := err.(HTTPErrorResponse)
	if !ok {
		he = &httpError{
			Code:    http.StatusInternalServerError,
			Message: err.Error(),
		}
	}

	msgObj := he.ErrorMessage()
	if msgObj == nil {
		msgObj = he
	}
	var resp []byte
	if b.Debug {
		resp, err = json.MarshalIndent(msgObj, "", "  ")
	} else {
		resp, err = json.Marshal(msgObj)
	}
	if err != nil {
		http.Error(b.W, err.Error(), http.StatusInternalServerError)
		return err
	}
	b.W.Header().Set("Content-Type", "application/json; charset=UTF-8")
	b.W.WriteHeader(he.StatusCode())
	b.W.Write(resp)
	return nil
}

var _ HTTPErrorResponse = &validateError{}

// Validator is an interface of request object validation.
type Validator interface {
	Validate(v interface{}) error
}

type validateError struct {
	Code   int   `json:"code"`
	Origin error `json:"-"`
}

func (ve *validateError) StatusCode() int {
	if her, ok := ve.Origin.(HTTPErrorResponse); ok {
		return her.StatusCode()
	}
	return ve.Code
}

func (ve *validateError) ErrorMessage() interface{} {
	if her, ok := ve.Origin.(HTTPErrorResponse); ok {
		return her.ErrorMessage()
	}
	return ve.Origin
}

func (ve *validateError) Error() string {
	if ve.Origin != nil {
		return ve.Origin.Error()
	}
	return fmt.Sprintf("status code %d: %v", ve.StatusCode(), ve.ErrorMessage())
}

type IgnoreValidateFunc func(argT reflect.Type, rv reflect.Value) bool

type RequestValidatorOption struct {
	IgnoreValidateFunc IgnoreValidateFunc
}

// RequestValidator checks request object validity.
func RequestValidator(validator Validator, options... *RequestValidatorOption) MiddlewareFunc {
	if validator == nil {
		v := golidator.NewValidator()
		v.SetTag("ucon")
		validator = v
	}

	ignoreFunc := func(options []*RequestValidatorOption, argT reflect.Type, rv reflect.Value) bool {
		for _, option := range options {
			if option.IgnoreValidateFunc != nil && option.IgnoreValidateFunc(argT, rv) {
				return true
			}
		}
		return false
	}

	return func(b *Bubble) error {
		for idx, argT := range b.ArgumentTypes {
			if httpReqType.AssignableTo(argT) {
				continue
			} else if httpRespType.AssignableTo(argT) {
				continue
			} else if contextType.AssignableTo(argT) {
				continue
			}

			rv := b.Arguments[idx]

			if ignoreFunc(options, argT, rv) {
				continue
			}

			if !rv.IsValid() || rv.IsNil() {
				continue
			}
			v := rv.Interface()
			err := validator.Validate(v)
			if herr, ok := err.(HTTPErrorResponse); ok && herr != nil {
				return err
			} else if gerr, ok := err.(*golidator.ErrorReport); ok && gerr != nil {
				return &validateError{Code: http.StatusBadRequest, Origin: gerr}
			} else if err != nil {
				return err
			}
		}

		return b.Next()
	}
}

// CSRFOption is options for CSRFProtect.
type CSRFOption struct {
	Salt              []byte
	SafeMethods       []string
	CookieName        string
	RequestHeaderName string
	GenerateCookie    func(r *http.Request) (*http.Cookie, error)
}

// CSRFProtect is a CSRF (Cross Site Request Forgery) prevention middleware.
func CSRFProtect(opts *CSRFOption) (MiddlewareFunc, error) {
	// default target is AngularJS
	// https://angular.io/docs/ts/latest/guide/security.html#!#http

	if opts == nil {
		return nil, errors.New("opts is required")
	}
	if len(opts.SafeMethods) == 0 {
		// from https://tools.ietf.org/html/rfc7231#section-4.2.2
		// Idempotent Methods
		opts.SafeMethods = []string{"GET", "HEAD", "OPTIONS", "TRACE"}
	}
	if opts.CookieName == "" {
		opts.CookieName = "XSRF-TOKEN"
	}
	if opts.RequestHeaderName == "" {
		opts.RequestHeaderName = "X-XSRF-TOKEN"
	}
	if opts.GenerateCookie == nil {
		if len(opts.Salt) == 0 {
			return nil, errors.New("opts.Salt is required")
		}
		opts.GenerateCookie = func(r *http.Request) (*http.Cookie, error) {
			b := make([]byte, 32)
			_, err := rand.Read(b)
			if err != nil {
				return nil, err
			}
			b = append(opts.Salt, b...)

			cookie := &http.Cookie{
				Name:     opts.CookieName,
				Value:    fmt.Sprintf("%x", sha256.Sum256([]byte(b))),
				MaxAge:   0,
				HttpOnly: false,
				Secure:   true,
			}

			return cookie, nil
		}
	}

	contains := func(strs []string, target string) bool {
		for _, str := range strs {
			if str == target {
				return true
			}
		}

		return false
	}

	middleware := func(b *Bubble) error {
		if contains(opts.SafeMethods, b.R.Method) {
			_, err := b.R.Cookie(opts.CookieName)
			if err == http.ErrNoCookie {
				cookie, err := opts.GenerateCookie(b.R)
				if err != nil {
					return err
				}
				http.SetCookie(b.W, cookie)
			} else if err != nil {
				return err
			}
		} else {
			csrfTokenRequest := b.R.Header.Get(opts.RequestHeaderName)
			csrfTokenCookie, _ := b.R.Cookie(opts.CookieName)
			if csrfTokenRequest == "" || csrfTokenCookie == nil {
				return ErrCSRFBadToken
			}
			if csrfTokenRequest != csrfTokenCookie.Value {
				return ErrCSRFBadToken
			}
		}

		err := b.Next()
		if err != nil {
			return err
		}

		return nil
	}

	return middleware, nil
}
