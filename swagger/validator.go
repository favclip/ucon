package swagger

import (
	"fmt"
	"reflect"

	"github.com/favclip/golidator"
	"github.com/favclip/ucon"
)

// DefaultValidator used in RequestValidator.
var DefaultValidator ucon.Validator

var _ ucon.HTTPErrorResponse = &validateError{}
var _ error = &validateError{}

type validateError struct {
	Code   int   `json:"code"`
	Origin error `json:"-"`
}

type validateMessage struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (ve *validateError) StatusCode() int {
	return ve.Code
}

func (ve *validateError) ErrorMessage() interface{} {
	return &validateMessage{
		Type:    "https://github.com/favclip/ucon#swagger-validate",
		Message: ve.Origin.Error(),
	}
}

func (ve *validateError) Error() string {
	if ve.Origin != nil {
		return ve.Origin.Error()
	}
	return fmt.Sprintf("status code %d: %v", ve.StatusCode(), ve.ErrorMessage())
}

// RequestValidator checks request object validity by swagger tag.
func RequestValidator(options... *ucon.RequestValidatorOption) ucon.MiddlewareFunc {
	return ucon.RequestValidator(DefaultValidator, options...)
}

func init() {
	v := &golidator.Validator{}
	v.SetTag("swagger")

	v.SetValidationFunc("req", golidator.ReqValidator)
	v.SetValidationFunc("d", golidator.DefaultValidator)
	v.SetValidationFunc("enum", golidator.EnumValidator)

	v.SetValidationFunc("min", golidator.MinValidator)
	v.SetValidationFunc("max", golidator.MaxValidator)
	v.SetValidationFunc("minLen", golidator.MinLenValidator)
	v.SetValidationFunc("maxLen", golidator.MaxLenValidator)
	v.SetValidationFunc("pattern", golidator.RegExpValidator)

	// ignore in=path, in=query pattern
	v.SetValidationFunc("in", func(param string, v reflect.Value) (golidator.ValidationResult, error) {
		return golidator.ValidationOK, nil
	})

	DefaultValidator = v
}
