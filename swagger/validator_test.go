package swagger

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/favclip/ucon"
)

type TargetRequestValidate struct {
	Text string `swagger:"enum=ok|ng"`
}

type IgnoreRequestValidate struct {
	Text string `swagger:"enum=ok|ng"`
}



func TestRequestValidator_valid(t *testing.T) {
	b, _ := ucon.MakeMiddlewareTestBed(t, RequestValidator(), func(req *TargetRequestValidate) {
	}, nil)
	b.Arguments[0] = reflect.ValueOf(&TargetRequestValidate{Text: "ok"})

	err := b.Next()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestRequestValidator_validWithIgnore(t *testing.T) {

	ignoreType := reflect.TypeOf((*IgnoreRequestValidate)(nil))
	b, _ := ucon.MakeMiddlewareTestBed(t, RequestValidator(&ucon.RequestValidatorOption{IgnoreValidateFunc: func(argT reflect.Type, rv reflect.Value) bool {
		if ignoreType.AssignableTo(argT) {
			return true
		}
		return false
	}}), func(req *TargetRequestValidate, req2 *IgnoreRequestValidate) {
	}, nil)
	b.Arguments[0] = reflect.ValueOf(&TargetRequestValidate{Text: "ok"})
	b.Arguments[1] = reflect.ValueOf(&IgnoreRequestValidate{Text: "bad"})

	err := b.Next()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestRequestValidator_invalid(t *testing.T) {
	b, _ := ucon.MakeMiddlewareTestBed(t, RequestValidator(), func(req *TargetRequestValidate) {
	}, nil)
	b.Arguments[0] = reflect.ValueOf(&TargetRequestValidate{Text: "invalid"})

	err := b.Next()
	if err == nil {
		t.Fatalf("unexpected: %v", err)
	}
	if v := err.(ucon.HTTPErrorResponse).StatusCode(); v != http.StatusBadRequest {
		t.Errorf("unexpected: %v", v)
	}
}

type TargetRequestPatternValidate struct {
	Text string `swagger:"pattern=\\d+"`
}

func TestRequestValidator_patternValid(t *testing.T) {
	b, _ := ucon.MakeMiddlewareTestBed(t, RequestValidator(), func(req *TargetRequestPatternValidate) {
	}, nil)
	b.Arguments[0] = reflect.ValueOf(&TargetRequestPatternValidate{Text: "123456789"})

	err := b.Next()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestRequestValidator_patternInvalid(t *testing.T) {
	b, _ := ucon.MakeMiddlewareTestBed(t, RequestValidator(), func(req *TargetRequestPatternValidate) {
	}, nil)
	b.Arguments[0] = reflect.ValueOf(&TargetRequestPatternValidate{Text: "invalid"})

	err := b.Next()
	if err == nil {
		t.Fatalf("unexpected: %v", err)
	}
	if v := err.(ucon.HTTPErrorResponse).StatusCode(); v != http.StatusBadRequest {
		t.Errorf("unexpected: %v", v)
	}
}
